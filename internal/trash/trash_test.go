package trash

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fixed(t time.Time) Option { return WithClock(func() time.Time { return t }) }

func TestMoveFile(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	tmp := t.TempDir()
	root := filepath.Join(tmp, "trash")
	src := filepath.Join(tmp, "local", "file.txt")
	_ = os.MkdirAll(filepath.Dir(src), 0o700)
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatalf("setup src: %v", err)
	}

	m := New(root, 30*24*time.Hour, fixed(now))
	dest, err := m.Move("docs/file.txt", src)
	if err != nil {
		t.Fatalf("Move(): %v", err)
	}

	want := filepath.Join(root, "2026-10-29T12-00", "docs", "file.txt")
	if dest != want {
		t.Errorf("Move() returned %q, want %q", dest, want)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("moved file missing: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source file still exists after Move")
	}
}

func TestMoveSameMinuteCollisionRenamesWithFullTimestamp(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	tmp := t.TempDir()
	root := filepath.Join(tmp, "trash")
	m := New(root, 30*24*time.Hour, fixed(now))

	makeSrc := func(name string) string {
		path := filepath.Join(tmp, name)
		_ = os.MkdirAll(filepath.Dir(path), 0o700)
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatalf("setup src %q: %v", name, err)
		}
		return path
	}

	first := makeSrc("a/file.txt")
	second := makeSrc("b/file.txt")

	dest1, err := m.Move("docs/file.txt", first)
	if err != nil {
		t.Fatalf("Move() first: %v", err)
	}
	dest2, err := m.Move("docs/file.txt", second)
	if err != nil {
		t.Fatalf("Move() second: %v", err)
	}

	if dest1 == dest2 {
		t.Fatalf("Move() returned identical paths %q for two colliding deletes", dest1)
	}
	if _, err := os.Stat(dest1); err != nil {
		t.Errorf("first moved file missing: %v", err)
	}
	if _, err := os.Stat(dest2); err != nil {
		t.Errorf("second moved file missing: %v", err)
	}
	if !exists(dest2) || filepath.Base(dest2) == "file.txt" {
		t.Errorf("second collision did not get a timestamped name: %q", dest2)
	}
}

func TestMoveDirectory(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	tmp := t.TempDir()
	root := filepath.Join(tmp, "trash")
	src := filepath.Join(tmp, "local", "dir")
	_ = os.MkdirAll(filepath.Join(src, "nested"), 0o700)
	if err := os.WriteFile(filepath.Join(src, "nested", "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	m := New(root, 30*24*time.Hour, fixed(now))
	dest, err := m.Move("dir", src)
	if err != nil {
		t.Fatalf("Move(): %v", err)
	}

	if _, err := os.Stat(filepath.Join(dest, "nested", "a.txt")); err != nil {
		t.Errorf("nested file missing after dir move: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source dir still exists after Move")
	}
}

func TestSweepRemovesBucketsOlderThanRetention(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	tmp := t.TempDir()
	root := filepath.Join(tmp, "trash")

	oldBucket := filepath.Join(root, "2026-09-01T12-00")
	newBucket := filepath.Join(root, "2026-10-29T11-00")
	_ = os.MkdirAll(oldBucket, 0o700)
	_ = os.MkdirAll(newBucket, 0o700)

	m := New(root, 30*24*time.Hour, fixed(now))
	if err := m.Sweep(); err != nil {
		t.Fatalf("Sweep(): %v", err)
	}

	if _, err := os.Stat(oldBucket); !os.IsNotExist(err) {
		t.Errorf("old bucket still exists after Sweep")
	}
	if _, err := os.Stat(newBucket); err != nil {
		t.Errorf("new bucket was incorrectly removed: %v", err)
	}
}

func TestSweepBoundaryExactlyAtCutoff(t *testing.T) {
	retention := 30 * 24 * time.Hour
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-retention)
	// A bucket at the exact cutoff minute is treated as before cutoff
	// because bucket precision truncates to the minute.
	bucketAtCutoff := filepath.Join(t.TempDir(), "trash", bucketName(cutoff))
	_ = os.MkdirAll(bucketAtCutoff, 0o700)

	m := New(filepath.Dir(bucketAtCutoff), retention, fixed(now))
	if err := m.Sweep(); err != nil {
		t.Fatalf("Sweep(): %v", err)
	}
	if _, err := os.Stat(bucketAtCutoff); !os.IsNotExist(err) {
		t.Errorf("bucket exactly at cutoff was not removed")
	}
}

func TestSweepNoRetentionKeepsEverything(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	root := filepath.Join(t.TempDir(), "trash")
	oldBucket := filepath.Join(root, "2026-01-01T00-00")
	_ = os.MkdirAll(oldBucket, 0o700)

	m := New(root, 0, fixed(now))
	if err := m.Sweep(); err != nil {
		t.Fatalf("Sweep(): %v", err)
	}
	if _, err := os.Stat(oldBucket); err != nil {
		t.Errorf("Sweep() with zero retention removed a bucket")
	}
}

func TestSweepMissingRootIsNoOp(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "does-not-exist"), 24*time.Hour)
	if err := m.Sweep(); err != nil {
		t.Fatalf("Sweep(): %v", err)
	}
}

func TestMoveFileCrossDevice(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	tmp := t.TempDir()
	root := filepath.Join(tmp, "trash")
	src := filepath.Join(tmp, "local", "file.txt")
	_ = os.MkdirAll(filepath.Dir(src), 0o700)
	if err := os.WriteFile(src, []byte("cross-device"), 0o600); err != nil {
		t.Fatalf("setup src: %v", err)
	}

	m := New(root, 30*24*time.Hour, fixed(now), WithRename(func(_, _ string) error {
		return os.ErrInvalid // simulate cross-device rename failure
	}))
	dest, err := m.Move("docs/file.txt", src)
	if err != nil {
		t.Fatalf("Move(): %v", err)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("moved file missing after copy fallback: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source file still exists after copy fallback")
	}
	data, err := os.ReadFile(dest)
	if err != nil || string(data) != "cross-device" {
		t.Errorf("copied content mismatch: %v, %q", err, data)
	}
}

func TestMoveReturnsErrorForMissingSource(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "trash"), 24*time.Hour)
	if _, err := m.Move("x.txt", "/nonexistent/isync/test"); err == nil {
		t.Fatal("Move() succeeded for a missing source")
	}
}

func TestRetentionDaysOption(t *testing.T) {
	root := filepath.Join(t.TempDir(), "trash")
	m := New(root, 7*24*time.Hour)
	if m.retention != 7*24*time.Hour {
		t.Errorf("retention = %v, want %v", m.retention, 7*24*time.Hour)
	}
}
