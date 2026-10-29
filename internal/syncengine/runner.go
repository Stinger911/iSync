package syncengine

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Stinger911/iSync/internal/apiclient"
	"github.com/Stinger911/iSync/internal/stateindex"
)

// Runner performs one sync pass for a folder pair: scan local and remote,
// diff against the last-known state, apply decisions, and update the state
// index. It is the I/O-wiring counterpart to the pure Diff function.
type Runner struct {
	client      Client
	state       *stateindex.Store
	trash       Trash
	maxFileSize int64
	hasher      func(string) (string, error)
}

// Client is the narrow remote API surface the runner needs.
type Client interface {
	List(ctx context.Context, session apiclient.Session, path string) ([]apiclient.Item, error)
	Fetch(ctx context.Context, session apiclient.Session, path string) (io.ReadCloser, error)
	Upload(ctx context.Context, session apiclient.Session, path string, content io.Reader, size int64) error
	Delete(ctx context.Context, session apiclient.Session, path string) error
}

// Trash abstracts the quarantine manager for the runner.
type Trash interface {
	Move(relPath, src string) (string, error)
}

// Option configures a Runner.
type Option func(*Runner)

// WithHasher overrides the content-hash function (used in tests).
func WithHasher(h func(string) (string, error)) Option {
	return func(r *Runner) { r.hasher = h }
}

// NewRunner returns a Runner. maxFileSize <= 0 disables the size gate.
func NewRunner(client Client, state *stateindex.Store, trash Trash, maxFileSize int64, opts ...Option) *Runner {
	r := &Runner{
		client:      client,
		state:       state,
		trash:       trash,
		maxFileSize: maxFileSize,
		hasher:      hashFile,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// SyncPair runs one full sync pass for the given pair and session.
func (r *Runner) SyncPair(ctx context.Context, pairID, localRoot, remoteRoot string, session apiclient.Session) error {
	local, err := r.scanLocal(localRoot)
	if err != nil {
		return fmt.Errorf("syncengine: scanning local: %w", err)
	}

	remote, err := r.scanRemote(ctx, session, remoteRoot)
	if err != nil {
		return fmt.Errorf("syncengine: scanning remote: %w", err)
	}

	// Collect all paths: current local, current remote, and previously known.
	paths := make(map[string]struct{})
	for p := range local {
		paths[p] = struct{}{}
	}
	for p := range remote {
		paths[p] = struct{}{}
	}
	if err := r.state.ForEach(pairID, func(p string, _ stateindex.Record) error {
		paths[p] = struct{}{}
		return nil
	}); err != nil {
		return fmt.Errorf("syncengine: enumerating state index: %w", err)
	}

	for p := range paths {
		if skipPath(p) {
			continue
		}

		priorIdx, _ := r.state.Get(pairID, p)
		prior := Record{
			LocalModified:  priorIdx.LocalModified,
			LocalSize:      priorIdx.LocalSize,
			LocalHash:      priorIdx.LocalHash,
			RemoteModified: priorIdx.RemoteModified,
			RemoteETag:     priorIdx.RemoteETag,
			LastSyncedAt:   priorIdx.LastSyncedAt,
		}
		localObs := local[p]
		remoteObs := remote[p]

		// Hash fallback: if Diff needs a hash to decide a local change,
		// compute it now.
		if _, err := Diff(prior, localObs, remoteObs, p); errors.Is(err, ErrHashRequired) {
			if localObs.Exists && !localObs.Modified.IsZero() {
				hash, err := r.hasher(filepath.Join(localRoot, p))
				if err != nil {
					return fmt.Errorf("syncengine: hashing %q: %w", p, err)
				}
				localObs.Hash = hash
			}
		}

		decision, err := Diff(prior, localObs, remoteObs, p)
		if err != nil {
			return fmt.Errorf("syncengine: diff %q: %w", p, err)
		}

		if decision.Action == ActionNoOp {
			continue
		}

		if err := r.apply(ctx, pairID, localRoot, remoteRoot, session, p, decision, localObs, remoteObs); err != nil {
			return fmt.Errorf("syncengine: applying %s to %q: %w", decision.Action, p, err)
		}
	}

	return nil
}

func skipPath(p string) bool {
	base := filepath.Base(p)
	return strings.HasPrefix(base, ".")
}

func (r *Runner) scanLocal(root string) (map[string]Observation, error) {
	out := make(map[string]Observation)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if skipPath(rel) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		out[rel] = Observation{
			Exists:   true,
			Modified: info.ModTime().Truncate(time.Second),
			Size:     info.Size(),
		}
		return nil
	})
	return out, err
}

func (r *Runner) scanRemote(ctx context.Context, session apiclient.Session, root string) (map[string]Observation, error) {
	out := make(map[string]Observation)
	err := r.scanRemoteRecursive(ctx, session, root, root, out)
	return out, err
}

func (r *Runner) scanRemoteRecursive(ctx context.Context, session apiclient.Session, root, p string, out map[string]Observation) error {
	items, err := r.client.List(ctx, session, p)
	if err != nil {
		return err
	}
	for _, item := range items {
		rel, err := relativeTo(root, item.Path)
		if err != nil {
			continue
		}
		if skipPath(rel) {
			continue
		}
		out[rel] = Observation{
			Exists:   true,
			Modified: item.ModifiedTime,
			Size:     item.Size,
			ETag:     item.ETag,
		}
		if item.IsDir {
			if err := r.scanRemoteRecursive(ctx, session, root, item.Path, out); err != nil {
				return err
			}
		}
	}
	return nil
}

func relativeTo(base, p string) (string, error) {
	cleanBase := filepath.Clean(strings.TrimPrefix(base, "/"))
	cleanP := filepath.Clean(strings.TrimPrefix(p, "/"))
	rel := strings.TrimPrefix(cleanP, cleanBase)
	if rel == cleanP && cleanBase != "" {
		return "", fmt.Errorf("path %q is not under %q", p, base)
	}
	rel = strings.TrimPrefix(rel, "/")
	return rel, nil
}

func (r *Runner) apply(ctx context.Context, pairID, localRoot, remoteRoot string, session apiclient.Session, p string, decision Decision, localObs, remoteObs Observation) error {
	localPath := filepath.Join(localRoot, p)
	remotePath := filepath.Join(remoteRoot, p)

	switch decision.Action {
	case ActionUpload, ActionConflictLocalWins:
		return r.upload(ctx, pairID, localPath, remotePath, session, p, localObs)
	case ActionDownload, ActionConflictRemoteWins:
		return r.download(ctx, pairID, localPath, remotePath, session, p, remoteObs)
	case ActionDeleteLocal:
		if _, err := r.trash.Move(p, localPath); err != nil {
			return err
		}
		return r.state.Delete(pairID, p)
	case ActionDeleteRemote:
		if err := r.client.Delete(ctx, session, remotePath); err != nil {
			return err
		}
		return r.state.Delete(pairID, p)
	default:
		return fmt.Errorf("unknown action %v", decision.Action)
	}
}

func (r *Runner) upload(ctx context.Context, pairID, localPath, remotePath string, session apiclient.Session, relPath string, obs Observation) error {
	if r.maxFileSize > 0 && obs.Size > r.maxFileSize {
		return fmt.Errorf("syncengine: %s exceeds max file size (%d > %d)", relPath, obs.Size, r.maxFileSize)
	}

	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()

	if err := r.client.Upload(ctx, session, remotePath, f, obs.Size); err != nil {
		return err
	}

	hash, err := r.hasher(localPath)
	if err != nil {
		return err
	}

	return r.state.Put(pairID, relPath, stateindex.Record{
		LocalModified:  obs.Modified,
		LocalSize:      obs.Size,
		LocalHash:      hash,
		RemoteModified: obs.Modified,
		RemoteETag:     obs.ETag,
		LastSyncedAt:   time.Now(),
	})
}

func (r *Runner) download(ctx context.Context, pairID, localPath, remotePath string, session apiclient.Session, relPath string, obs Observation) error {
	rc, err := r.client.Fetch(ctx, session, remotePath)
	if err != nil {
		return err
	}
	defer rc.Close()

	if err := os.MkdirAll(filepath.Dir(localPath), 0o700); err != nil {
		return err
	}

	tmp := localPath + ".isync.tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}

	written, err := io.Copy(out, rc)
	if err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	if err := os.Rename(tmp, localPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	_ = os.Chtimes(localPath, obs.Modified, obs.Modified)

	return r.state.Put(pairID, relPath, stateindex.Record{
		LocalModified:  obs.Modified,
		LocalSize:      written,
		RemoteModified: obs.Modified,
		RemoteETag:     obs.ETag,
		LastSyncedAt:   time.Now(),
	})
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
