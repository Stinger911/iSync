package syncengine

import (
	"errors"
	"time"
)

// Action is the outcome the sync engine decides for a single path.
type Action int

const (
	// ActionNoOp means both sides already match the last-known state
	// (or both sides deleted the path); nothing to do.
	ActionNoOp Action = iota

	// ActionUpload means the local side changed and the remote side did
	// not; upload the local content to iCloud.
	ActionUpload

	// ActionDownload means the remote side changed and the local side did
	// not; download the remote content locally.
	ActionDownload

	// ActionDeleteLocal means the remote side deleted the path; delete
	// the local copy (via the trash path, per FR-7).
	ActionDeleteLocal

	// ActionDeleteRemote means the local side deleted the path; delete
	// the remote copy.
	ActionDeleteRemote

	// ActionConflictRemoteWins means both sides changed but the remote
	// side has the newer modification time (last-write-wins): delete the
	// local copy to trash and download the remote version.
	ActionConflictRemoteWins

	// ActionConflictLocalWins means both sides changed but the local
	// side has the newer modification time (last-write-wins): upload the
	// local version over the remote.
	ActionConflictLocalWins
)

// String returns a human-readable name for the action.
func (a Action) String() string {
	switch a {
	case ActionNoOp:
		return "no-op"
	case ActionUpload:
		return "upload"
	case ActionDownload:
		return "download"
	case ActionDeleteLocal:
		return "delete-local"
	case ActionDeleteRemote:
		return "delete-remote"
	case ActionConflictRemoteWins:
		return "conflict-remote-wins"
	case ActionConflictLocalWins:
		return "conflict-local-wins"
	default:
		return "unknown"
	}
}

// Decision is the result of diffing one path.
type Decision struct {
	Action Action
	// Path is the relative path (within the folder pair) this decision
	// applies to.
	Path string
}

// Observation is the current metadata for a file on one side (local or
// remote). Exists==false means the path is absent on that side.
type Observation struct {
	// Exists reports whether the path is present on this side.
	Exists bool
	// Modified is the file's modification time.
	Modified time.Time
	// Size is the file size in bytes.
	Size int64
	// Hash is the content hash (SHA-256 hex). It is only needed to
	// disambiguate a local change when Size and Modified are unchanged
	// from the last-known record (see ErrHashRequired).
	Hash string
	// ETag is the remote-side checksum/etag, when known.
	ETag string
}

// Record is the last-known, in-sync state for a path, persisted by
// internal/stateindex. A zero-value Record means "never seen before".
type Record struct {
	LocalModified  time.Time
	LocalSize      int64
	LocalHash      string
	RemoteModified time.Time
	RemoteETag     string
	LastSyncedAt   time.Time
}

// ErrHashRequired is returned by Diff when a definitive local-change
// answer needs the current content hash (local Size and Modified are
// unchanged from the last record, but the content may still differ — a
// same-size, same-second rewrite). The caller computes the hash, sets it
// on the observation, and retries.
var ErrHashRequired = errors.New("syncengine: content hash required to decide")

// Diff decides what to do for a single path, given the last-known record
// and the current local and remote observations. It is a pure function
// (no I/O), so it can be tested exhaustively. See plan.md Phase 4.
func Diff(prior Record, local, remote Observation, path string) (Decision, error) {
	decision := func(a Action) Decision { return Decision{Action: a, Path: path} }

	localExists := local.Exists
	remoteExists := remote.Exists

	// Both sides deleted: converge on nothing.
	if !localExists && !remoteExists {
		return decision(ActionNoOp), nil
	}

	// A deletion only propagates when the now-missing side was previously
	// synced (recorded). Otherwise a one-sided presence is a brand-new
	// file, handled below. The action names the side to delete: a missing
	// local file (present remote) means the remote copy must be deleted;
	// a missing remote file means the local copy must be deleted.
	if !localExists && remoteExists {
		if !prior.LocalModified.IsZero() {
			return decision(ActionDeleteRemote), nil
		}
		return decision(ActionDownload), nil // new remote file
	}
	if localExists && !remoteExists {
		if !prior.RemoteModified.IsZero() {
			return decision(ActionDeleteLocal), nil
		}
		return decision(ActionUpload), nil // new local file
	}

	// Both sides present. Determine change status per side.
	localChanged, err := localChangedSince(prior, local)
	if err != nil {
		return Decision{}, err
	}
	remoteChanged := remoteChangedSince(prior, remote)

	switch {
	case !localChanged && !remoteChanged:
		return decision(ActionNoOp), nil
	case localChanged && !remoteChanged:
		return decision(ActionUpload), nil
	case !localChanged && remoteChanged:
		return decision(ActionDownload), nil
	}

	// Both changed: resolve last-write-wins by modification time.
	switch {
	case remote.Modified.After(local.Modified):
		return decision(ActionConflictRemoteWins), nil
	default:
		// Ties and local-newer both favor the local side.
		return decision(ActionConflictLocalWins), nil
	}
}

// localChangedSince reports whether the local observation differs from the
// last-known record. If Size and Modified are unchanged but the hash is
// absent, it returns ErrHashRequired rather than guessing.
func localChangedSince(prior Record, local Observation) (bool, error) {
	// Never seen before: any local file counts as a change.
	if prior.LocalModified.IsZero() {
		return true, nil
	}
	if !local.Modified.Equal(prior.LocalModified) || local.Size != prior.LocalSize {
		return true, nil
	}
	// Size + mtime match; only a content hash can tell us whether the
	// bytes changed (e.g. a same-size, same-second rewrite).
	if local.Hash == "" {
		return false, ErrHashRequired
	}
	return local.Hash != prior.LocalHash, nil
}

// remoteChangedSince reports whether the remote observation differs from
// the last-known record, using etag when available and falling back to
// modification time.
func remoteChangedSince(prior Record, remote Observation) bool {
	if prior.RemoteModified.IsZero() {
		return true
	}
	if remote.ETag != "" && prior.RemoteETag != "" {
		return remote.ETag != prior.RemoteETag
	}
	return !remote.Modified.Equal(prior.RemoteModified)
}
