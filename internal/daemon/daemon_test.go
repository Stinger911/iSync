package daemon

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Stinger911/iSync/internal/apiclient"
	"github.com/Stinger911/iSync/internal/apiclient/fake"
	"github.com/Stinger911/iSync/internal/config"
	"github.com/Stinger911/iSync/internal/ipc"
)

func TestDaemonSyncsOnLocalChange(t *testing.T) {
	client := fake.NewServer()
	client.AddAccount("test@example.com", "pass")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, _, err := client.Authenticate(ctx, "test@example.com", "pass")
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if err := client.Upload(ctx, session, "/iCloud/docs/.isync-root", bytes.NewReader(nil), 0); err != nil {
		t.Fatalf("seed root: %v", err)
	}

	tmp := t.TempDir()
	localRoot := filepath.Join(tmp, "local")
	_ = os.MkdirAll(localRoot, 0o700)

	cfg := config.Default()
	cfg.Pairs = []config.Pair{{
		ID:         "docs",
		LocalPath:  localRoot,
		RemotePath: "/iCloud/docs",
		ICloudPath: "/iCloud/docs",
		Enabled:    true,
	}}
	cfg.Daemon.PollIntervalBaseline = config.Duration(60 * time.Second)
	cfg.Daemon.PollIntervalActive = config.Duration(1 * time.Second)
	cfg.Daemon.PollIntervalIdle = config.Duration(5 * time.Second)

	d, err := NewWithPaths(cfg, session, filepath.Join(tmp, "state.db"), filepath.Join(tmp, "trash"), filepath.Join(tmp, "status.json"))
	if err != nil {
		t.Fatalf("NewWithPaths: %v", err)
	}
	defer d.Close()

	// Inject the fake client after construction.
	d.client = client
	d.status = ipc.NewStatusStore(filepath.Join(tmp, "status.json"))

	// Start daemon briefly.
	done := make(chan struct{})
	go func() {
		_ = d.Run()
		close(done)
	}()

	// Wait until the initial status is written, then record baseline sync time.
	var baselineSync time.Time
	for start := time.Now(); time.Since(start) < 5*time.Second; {
		status, err := d.status.Read()
		if err != nil {
			t.Fatalf("read status: %v", err)
		}
		if ps, ok := status.PairStatuses["docs"]; ok {
			baselineSync = ps.LastSyncAt
			if !baselineSync.IsZero() {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if baselineSync.IsZero() {
		t.Fatal("daemon never wrote initial status")
	}

	// Write a local file and wait for a *new* LastSyncAt.
	if err := os.WriteFile(filepath.Join(localRoot, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write local: %v", err)
	}

	var synced bool
	for start := time.Now(); time.Since(start) < 10*time.Second; {
		status, err := d.status.Read()
		if err != nil {
			t.Fatalf("read status: %v", err)
		}
		if ps, ok := status.PairStatuses["docs"]; ok {
			if ps.OK && ps.LastSyncAt.After(baselineSync) {
				synced = true
				break
			}
			if !ps.OK {
				t.Fatalf("pair error: %s", ps.Error)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !synced {
		t.Fatal("daemon did not resync after local change")
	}

	// Wait until the remote file is actually observable.
	for start := time.Now(); time.Since(start) < 5*time.Second; {
		if r, err := client.Fetch(ctx, session, "/iCloud/docs/a.txt"); err == nil {
			defer r.Close()
			got, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("read remote: %v", err)
			}
			if string(got) != "hello" {
				t.Fatalf("remote content = %q, want hello", got)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("uploaded file not visible on remote after sync")
}

// NewWithClient is a test helper that builds a Daemon with an injected client.
func NewWithClient(cfg config.Config, session apiclient.Session, client apiclient.Client, statusPath string) (*Daemon, error) {
	d, err := NewWithPaths(cfg, session, "", "", statusPath)
	if err != nil {
		return nil, err
	}
	d.client = client
	d.status = ipc.NewStatusStore(statusPath)
	return d, nil
}
