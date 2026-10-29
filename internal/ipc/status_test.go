package ipc

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStatusStoreRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	store := NewStatusStore(filepath.Join(tmp, "status.json"))

	status := Status{
		UpdatedAt: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC),
		Healthy:   true,
		PairStatuses: map[string]PairStatus{
			"docs": {OK: true},
		},
	}

	if err := store.Write(status); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := store.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !got.Healthy {
		t.Fatal("expected healthy status")
	}
	if got.PairStatuses["docs"].OK != true {
		t.Fatalf("pair status = %+v, want OK", got.PairStatuses["docs"])
	}
}

func TestDefaultStatusPathRespectsXDG(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	path, err := DefaultStatusPath()
	if err != nil {
		t.Fatalf("DefaultStatusPath: %v", err)
	}
	if want := filepath.Join("/run/user/1000", "isync", "status.json"); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestReadMissingReturnsEmptyStatus(t *testing.T) {
	store := NewStatusStore(filepath.Join(t.TempDir(), "missing", "status.json"))
	got, err := store.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Healthy || got.Message != "" || len(got.PairStatuses) != 0 {
		t.Fatalf("expected empty status, got %+v", got)
	}
}
