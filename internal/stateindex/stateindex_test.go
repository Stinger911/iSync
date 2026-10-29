package stateindex

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestPutGetRoundTrip(t *testing.T) {
	s := openTemp(t)

	in := Record{
		LocalModified:  time.Now().Truncate(time.Second),
		LocalSize:      1234,
		LocalHash:      "deadbeef",
		RemoteModified: time.Now().Truncate(time.Second),
		RemoteETag:     "etag-1",
		LastSyncedAt:   time.Now().Truncate(time.Second),
	}
	if err := s.Put("docs", "sub/file.txt", in); err != nil {
		t.Fatalf("Put(): %v", err)
	}

	got, err := s.Get("docs", "sub/file.txt")
	if err != nil {
		t.Fatalf("Get(): %v", err)
	}
	if !got.LocalModified.Equal(in.LocalModified) || got.LocalSize != in.LocalSize ||
		got.LocalHash != in.LocalHash || !got.RemoteModified.Equal(in.RemoteModified) ||
		got.RemoteETag != in.RemoteETag || !got.LastSyncedAt.Equal(in.LastSyncedAt) {
		t.Errorf("Get() = %+v, want %+v", got, in)
	}
}

func TestGetMissingReturnsErrNotFound(t *testing.T) {
	s := openTemp(t)

	if _, err := s.Get("docs", "missing.txt"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get() error = %v, want ErrNotFound", err)
	}
}

func TestGetMissingPairBucket(t *testing.T) {
	s := openTemp(t)

	if _, err := s.Get("no-such-pair", "x.txt"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get() error = %v, want ErrNotFound", err)
	}
}

func TestPutOverwrites(t *testing.T) {
	s := openTemp(t)

	if err := s.Put("docs", "a.txt", Record{LocalSize: 1}); err != nil {
		t.Fatalf("Put() first: %v", err)
	}
	if err := s.Put("docs", "a.txt", Record{LocalSize: 2}); err != nil {
		t.Fatalf("Put() second: %v", err)
	}

	got, err := s.Get("docs", "a.txt")
	if err != nil {
		t.Fatalf("Get(): %v", err)
	}
	if got.LocalSize != 2 {
		t.Errorf("Get() LocalSize = %d, want 2", got.LocalSize)
	}
}

func TestDelete(t *testing.T) {
	s := openTemp(t)

	if err := s.Put("docs", "a.txt", Record{LocalSize: 1}); err != nil {
		t.Fatalf("Put(): %v", err)
	}
	if err := s.Delete("docs", "a.txt"); err != nil {
		t.Fatalf("Delete(): %v", err)
	}
	if _, err := s.Get("docs", "a.txt"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get() after Delete = %v, want ErrNotFound", err)
	}

	// Deleting from a missing bucket/record is a no-op, not an error.
	if err := s.Delete("docs", "a.txt"); err != nil {
		t.Errorf("Delete() on already-missing record = %v, want nil", err)
	}
	if err := s.Delete("no-such-pair", "x.txt"); err != nil {
		t.Errorf("Delete() on missing pair = %v, want nil", err)
	}
}

func TestForEach(t *testing.T) {
	s := openTemp(t)

	keys := []string{"a.txt", "b.txt", "c.txt"}
	for i, k := range keys {
		if err := s.Put("docs", k, Record{LocalSize: int64(i)}); err != nil {
			t.Fatalf("Put(%q): %v", k, err)
		}
	}

	var seen []string
	err := s.ForEach("docs", func(relPath string, _ Record) error {
		seen = append(seen, relPath)
		return nil
	})
	if err != nil {
		t.Fatalf("ForEach(): %v", err)
	}
	if len(seen) != len(keys) {
		t.Fatalf("ForEach() visited %d records, want %d", len(seen), len(keys))
	}
	for i, k := range keys {
		if seen[i] != k {
			t.Errorf("ForEach() order[%d] = %q, want %q", i, seen[i], k)
		}
	}
}

func TestPairsAreIsolated(t *testing.T) {
	s := openTemp(t)

	if err := s.Put("pair-a", "x.txt", Record{LocalSize: 1}); err != nil {
		t.Fatalf("Put(pair-a): %v", err)
	}
	if err := s.Put("pair-b", "x.txt", Record{LocalSize: 2}); err != nil {
		t.Fatalf("Put(pair-b): %v", err)
	}

	a, err := s.Get("pair-a", "x.txt")
	if err != nil || a.LocalSize != 1 {
		t.Errorf("Get(pair-a) = %+v, %v; want LocalSize 1", a, err)
	}
	b, err := s.Get("pair-b", "x.txt")
	if err != nil || b.LocalSize != 2 {
		t.Errorf("Get(pair-b) = %+v, %v; want LocalSize 2", b, err)
	}
}
