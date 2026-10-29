package syncengine

import (
	"errors"
	"testing"
	"time"
)

var (
	t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 = t0.Add(time.Minute)
	t2 = t0.Add(2 * time.Minute)
)

func obs(mod time.Time, size int64) Observation {
	return Observation{Exists: true, Modified: mod, Size: size}
}

func obsHash(mod time.Time, size int64, hash string) Observation {
	return Observation{Exists: true, Modified: mod, Size: size, Hash: hash}
}

func obsEtag(mod time.Time, etag string) Observation {
	return Observation{Exists: true, Modified: mod, ETag: etag}
}

func TestDiff(t *testing.T) {
	tests := []struct {
		name   string
		prior  Record
		local  Observation
		remote Observation
		want   Action
	}{
		{"new local file", Record{}, obs(t1, 10), Observation{}, ActionUpload},
		{"new remote file", Record{}, Observation{}, obs(t1, 10), ActionDownload},
		{"local-only change", Record{LocalModified: t0, LocalSize: 10, RemoteModified: t0, RemoteETag: "e0"}, obs(t1, 10), obs(t0, 10), ActionUpload},
		{"remote-only change by etag", Record{LocalModified: t0, LocalSize: 10, LocalHash: "h0", RemoteModified: t0, RemoteETag: "e0"}, obsHash(t0, 10, "h0"), obsEtag(t0, "e1"), ActionDownload},
		{"remote-only change by mtime", Record{LocalModified: t0, LocalSize: 10, LocalHash: "h0", RemoteModified: t0}, obsHash(t0, 10, "h0"), obs(t1, 10), ActionDownload},
		{"both changed, remote newer", Record{LocalModified: t0, LocalSize: 10, LocalHash: "h0", RemoteModified: t0, RemoteETag: "e0"}, obsHash(t1, 20, "h1"), obsEtag(t2, "e1"), ActionConflictRemoteWins},
		{"both changed, local newer", Record{LocalModified: t0, LocalSize: 10, LocalHash: "h0", RemoteModified: t0, RemoteETag: "e0"}, obsHash(t2, 20, "h1"), obsEtag(t1, "e1"), ActionConflictLocalWins},
		{"both changed, tie favors local", Record{LocalModified: t0, LocalSize: 10, LocalHash: "h0", RemoteModified: t0, RemoteETag: "e0"}, obsHash(t1, 20, "h1"), obsEtag(t1, "e1"), ActionConflictLocalWins},
		{"remote deleted", Record{LocalModified: t0, LocalSize: 10, LocalHash: "h0", RemoteModified: t0, RemoteETag: "e0"}, obsHash(t0, 10, "h0"), Observation{}, ActionDeleteLocal},
		{"local deleted", Record{LocalModified: t0, LocalSize: 10, LocalHash: "h0", RemoteModified: t0, RemoteETag: "e0"}, Observation{}, obsEtag(t0, "e0"), ActionDeleteRemote},
		{"both deleted", Record{LocalModified: t0, LocalSize: 10, LocalHash: "h0", RemoteModified: t0, RemoteETag: "e0"}, Observation{}, Observation{}, ActionNoOp},
		{"both unchanged, hash match", Record{LocalModified: t0, LocalSize: 10, LocalHash: "h0", RemoteModified: t0, RemoteETag: "e0"}, obsHash(t0, 10, "h0"), obsEtag(t0, "e0"), ActionNoOp},
		{"both unchanged, same-size rewrite", Record{LocalModified: t0, LocalSize: 10, LocalHash: "h0", RemoteModified: t0, RemoteETag: "e0"}, obsHash(t0, 10, "h1"), obsEtag(t0, "e0"), ActionUpload},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Diff(tc.prior, tc.local, tc.remote, "file.txt")
			if err != nil {
				t.Fatalf("Diff() error = %v", err)
			}
			if got.Action != tc.want {
				t.Errorf("Diff() action = %v, want %v", got.Action, tc.want)
			}
			if got.Path != "file.txt" {
				t.Errorf("Diff() path = %q, want %q", got.Path, "file.txt")
			}
		})
	}
}

func TestDiffRequiresHashWhenAmbiguous(t *testing.T) {
	prior := Record{LocalModified: t0, LocalSize: 10, LocalHash: "h0", RemoteModified: t0, RemoteETag: "e0"}
	// Same size and mtime as prior, but no current hash supplied.
	local := obs(t0, 10)
	_, err := Diff(prior, local, obsEtag(t0, "e0"), "file.txt")
	if !errors.Is(err, ErrHashRequired) {
		t.Fatalf("Diff() error = %v, want ErrHashRequired", err)
	}
}

func TestDiffNewLocalFileDoesNotRequireHash(t *testing.T) {
	// A brand-new local file (no prior record) needs no hash to decide
	// upload.
	got, err := Diff(Record{}, obs(t1, 10), Observation{}, "file.txt")
	if err != nil {
		t.Fatalf("Diff() error = %v, want nil", err)
	}
	if got.Action != ActionUpload {
		t.Errorf("Diff() action = %v, want %v", got.Action, ActionUpload)
	}
}
