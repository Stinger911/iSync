package watcher

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestAddWatchDetectsFileChange(t *testing.T) {
	tmp := t.TempDir()
	mgr := NewManager(50 * time.Millisecond)
	defer mgr.Close()

	var mu sync.Mutex
	var gotID string
	var gotPaths []string
	done := make(chan struct{})

	_, err := mgr.Add("docs", tmp, func(id string, paths []string) {
		mu.Lock()
		gotID = id
		gotPaths = paths
		mu.Unlock()
		close(done)
	})
	if err != nil {
		t.Fatalf("Add(): %v", err)
	}

	file := filepath.Join(tmp, "file.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for watcher callback")
	}

	mu.Lock()
	defer mu.Unlock()
	if gotID != "docs" {
		t.Errorf("pair id = %q, want docs", gotID)
	}
	if len(gotPaths) != 1 || gotPaths[0] != "file.txt" {
		t.Errorf("paths = %v, want [file.txt]", gotPaths)
	}
}

func TestRemoveStopsWatching(t *testing.T) {
	tmp := t.TempDir()
	mgr := NewManager(50 * time.Millisecond)

	called := make(chan struct{})
	_, err := mgr.Add("docs", tmp, func(string, []string) { close(called) })
	if err != nil {
		t.Fatalf("Add(): %v", err)
	}

	if err := mgr.Remove("docs"); err != nil {
		t.Fatalf("Remove(): %v", err)
	}

	file := filepath.Join(tmp, "file.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case <-called:
		t.Fatal("callback fired after Remove")
	case <-time.After(200 * time.Millisecond):
		// expected
	}
}

func TestPathsResetsDirtySet(t *testing.T) {
	tmp := t.TempDir()
	mgr := NewManager(10 * time.Millisecond)
	defer mgr.Close()

	var mu sync.Mutex
	var calls int
	var firstCall []string
	done := make(chan struct{})
	w, err := mgr.Add("docs", tmp, func(id string, paths []string) {
		mu.Lock()
		if calls == 0 {
			firstCall = paths
		}
		calls++
		callCount := calls
		mu.Unlock()
		if callCount == 2 {
			close(done)
		}
	})
	if err != nil {
		t.Fatalf("Add(): %v", err)
	}

	if err := os.WriteFile(filepath.Join(tmp, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Wait enough for the first debounce window to fire.
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	if len(firstCall) != 1 || firstCall[0] != "a.txt" {
		t.Fatalf("first callback paths = %v, want [a.txt]", firstCall)
	}
	mu.Unlock()

	if err := os.WriteFile(filepath.Join(tmp, "b.txt"), []byte("b"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for second callback")
	}

	got := w.Paths()
	if got != nil {
		t.Fatalf("Paths() after callbacks = %v, want nil", got)
	}
}
