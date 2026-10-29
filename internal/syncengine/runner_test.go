package syncengine

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Stinger911/iSync/internal/apiclient"
	"github.com/Stinger911/iSync/internal/apiclient/fake"
	"github.com/Stinger911/iSync/internal/stateindex"
	"github.com/Stinger911/iSync/internal/trash"
)

func TestRunnerUploadsNewLocalFile(t *testing.T) {
	client := fake.NewServer()
	client.AddAccount("test@example.com", "pass")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, _, err := client.Authenticate(ctx, "test@example.com", "pass")
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}

	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "state.db")
	state, err := stateindex.Open(dbPath)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = state.Close() })

	trashDir := filepath.Join(tmp, "trash")
	mgr := trash.New(trashDir, 30*24*time.Hour)

	localRoot := filepath.Join(tmp, "local")
	remoteRoot := "/iCloud/docs"
	_ = os.MkdirAll(localRoot, 0o700)
	if err := client.Upload(ctx, session, "/iCloud/docs/.isync-root", bytes.NewReader(nil), 0); err != nil {
		t.Fatalf("seed remote root: %v", err)
	}

	if err := os.WriteFile(filepath.Join(localRoot, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write local: %v", err)
	}

	runner := NewRunner(client, state, mgr, 0)
	if err := runner.SyncPair(ctx, "docs", localRoot, remoteRoot, session); err != nil {
		t.Fatalf("SyncPair: %v", err)
	}

	rc, err := client.Fetch(ctx, session, "/iCloud/docs/a.txt")
	if err != nil {
		t.Fatalf("fetch remote: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read remote: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("remote content = %q, want hello", got)
	}

	rec, err := state.Get("docs", "a.txt")
	if err != nil {
		t.Fatalf("state get: %v", err)
	}
	if rec.LocalSize != 5 || rec.LocalHash == "" {
		t.Fatalf("record = %+v, want size=5 and non-empty hash", rec)
	}
}

func TestRunnerDownloadsNewRemoteFile(t *testing.T) {
	client := fake.NewServer()
	client.AddAccount("test@example.com", "pass")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, _, err := client.Authenticate(ctx, "test@example.com", "pass")
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}

	if err := client.Upload(ctx, session, "/iCloud/docs/b.txt", bytes.NewReader([]byte("remote")), 6); err != nil {
		t.Fatalf("seed upload: %v", err)
	}

	tmp := t.TempDir()
	localRoot := filepath.Join(tmp, "local")
	_ = os.MkdirAll(localRoot, 0o700)
	dbPath := filepath.Join(tmp, "state.db")
	state, err := stateindex.Open(dbPath)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = state.Close() })

	trashDir := filepath.Join(tmp, "trash")
	mgr := trash.New(trashDir, 30*24*time.Hour)

	runner := NewRunner(client, state, mgr, 0)
	if err := runner.SyncPair(ctx, "docs", localRoot, "/iCloud/docs", session); err != nil {
		t.Fatalf("SyncPair: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(localRoot, "b.txt"))
	if err != nil {
		t.Fatalf("read local: %v", err)
	}
	if string(got) != "remote" {
		t.Fatalf("local content = %q, want remote", got)
	}

	rec, err := state.Get("docs", "b.txt")
	if err != nil {
		t.Fatalf("state get: %v", err)
	}
	if rec.LocalSize != 6 {
		t.Fatalf("record size = %d, want 6", rec.LocalSize)
	}
}

func TestRunnerPropagatesLocalDeleteToRemote(t *testing.T) {
	client := fake.NewServer()
	client.AddAccount("test@example.com", "pass")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, _, err := client.Authenticate(ctx, "test@example.com", "pass")
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}

	if err := client.Upload(ctx, session, "/iCloud/docs/c.txt", bytes.NewReader([]byte("x")), 1); err != nil {
		t.Fatalf("seed upload: %v", err)
	}

	tmp := t.TempDir()
	localRoot := filepath.Join(tmp, "local")
	_ = os.MkdirAll(localRoot, 0o700)
	dbPath := filepath.Join(tmp, "state.db")
	state, err := stateindex.Open(dbPath)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = state.Close() })

	mt := time.Now().Truncate(time.Second)
	if err := state.Put("docs", "c.txt", stateindex.Record{LocalModified: mt, LocalSize: 1, RemoteModified: mt}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	trashDir := filepath.Join(tmp, "trash")
	mgr := trash.New(trashDir, 30*24*time.Hour)

	runner := NewRunner(client, state, mgr, 0)
	if err := runner.SyncPair(ctx, "docs", localRoot, "/iCloud/docs", session); err != nil {
		t.Fatalf("SyncPair: %v", err)
	}

	_, err = client.Fetch(ctx, session, "/iCloud/docs/c.txt")
	if !errors.Is(err, apiclient.ErrNotFound) {
		t.Fatalf("expected remote delete, got fetch err = %v", err)
	}

	if _, err := state.Get("docs", "c.txt"); err == nil {
		t.Fatal("state record should be deleted")
	}
}
