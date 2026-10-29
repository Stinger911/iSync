package trash

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Manager handles quarantine moves and retention cleanup for deleted
// synced content. Every delete path the sync engine executes goes
// through Move, never direct os.Remove, per FR-7 and plan.md Phase 5.
type Manager struct {
	root       string
	retention  time.Duration
	now        func() time.Time
	mkdirAll   func(string, os.FileMode) error
	removeAll  func(string) error
	rename     func(string, string) error
	openSource func(string) (*os.File, error)
	createDest func(string) (*os.File, error)
}

// Option configures a Manager.
type Option func(*Manager)

// WithRename overrides the rename function (used in tests to simulate
// cross-device moves).
func WithRename(rename func(string, string) error) Option {
	return func(m *Manager) { m.rename = rename }
}

// WithClock overrides the time source (used in tests).
func WithClock(now func() time.Time) Option {
	return func(m *Manager) { m.now = now }
}

// New returns a Manager rooted at root (e.g. ~/.local/share/isync/trash).
// Retention controls how long quarantined content is kept; entries older
// than retention are removed by Sweep. Use 0 to keep forever.
func New(root string, retention time.Duration, opts ...Option) *Manager {
	m := &Manager{
		root:      root,
		retention: retention,
		now:       time.Now,
		mkdirAll:  os.MkdirAll,
		removeAll: os.RemoveAll,
		rename:    os.Rename,
		openSource: func(path string) (*os.File, error) {
			return os.Open(path)
		},
		createDest: func(path string) (*os.File, error) {
			return os.Create(path)
		},
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Move moves src (a file or directory) into the trash directory under a
// minute-prefixed bucket, preserving the relative path structure. The
// resulting quarantine path is returned. If dest already exists within
// the same minute, the base name is renamed with the full nanosecond
// timestamp appended so no data is overwritten.
func (m *Manager) Move(relPath, src string) (string, error) {
	now := m.now()
	bucket := bucketName(now)
	dir := filepath.Join(m.root, bucket, filepath.Dir(relPath))
	if err := m.mkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("trash: creating quarantine dir: %w", err)
	}

	base := filepath.Base(relPath)
	dest := filepath.Join(dir, base)
	if exists(dest) {
		dest = filepath.Join(dir, uniqueName(base, now))
	}

	if err := movePath(src, dest, m.rename, m.openSource, m.createDest, m.mkdirAll); err != nil {
		return "", err
	}
	return dest, nil
}

// Sweep removes trash entries whose bucket timestamp is older than the
// configured retention. It walks the immediate children of root; each
// child is expected to be named in Move's minute-bucket format.
func (m *Manager) Sweep() error {
	if m.retention <= 0 {
		return nil
	}

	entries, err := os.ReadDir(m.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("trash: reading trash root: %w", err)
	}

	cutoff := m.now().Add(-m.retention)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		t, err := parseBucket(e.Name())
		if err != nil {
			continue // ignore non-bucket entries
		}
		if !t.After(cutoff) {
			if err := m.removeAll(filepath.Join(m.root, e.Name())); err != nil {
				return fmt.Errorf("trash: removing old bucket %q: %w", e.Name(), err)
			}
		}
	}
	return nil
}

func bucketName(t time.Time) string {
	return t.Format("2006-01-02T15-04")
}

func uniqueName(base string, t time.Time) string {
	ext := filepath.Ext(base)
	stem := base[:len(base)-len(ext)]
	return fmt.Sprintf("%s_%s%s", stem, t.Format("20060102T150405.000000000"), ext)
}

func parseBucket(name string) (time.Time, error) {
	return time.Parse("2006-01-02T15-04", name)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// movePath moves src to dest. For directories it copies recursively then
// removes src; for files it renames when possible, falling back to copy+unlink
// across filesystem boundaries.
func movePath(src, dest string, rename func(string, string) error, openSource, createDest func(string) (*os.File, error), mkdirAll func(string, os.FileMode) error) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("trash: stat source %q: %w", src, err)
	}

	if info.IsDir() {
		return moveDir(src, dest, rename, openSource, createDest, mkdirAll)
	}

	if err := rename(src, dest); err == nil {
		return nil
	}

	// Cross-device or otherwise non-renamable: copy and remove.
	return copyAndRemoveFile(src, dest, openSource, createDest)
}

func moveDir(src, dest string, rename func(string, string) error, openSource, createDest func(string) (*os.File, error), mkdirAll func(string, os.FileMode) error) error {
	if err := mkdirAll(dest, 0o700); err != nil {
		return fmt.Errorf("trash: creating dir %q: %w", dest, err)
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("trash: reading dir %q: %w", src, err)
	}
	for _, e := range entries {
		if err := movePath(filepath.Join(src, e.Name()), filepath.Join(dest, e.Name()), rename, openSource, createDest, mkdirAll); err != nil {
			return err
		}
	}
	if err := os.Remove(src); err != nil {
		return fmt.Errorf("trash: removing moved dir %q: %w", src, err)
	}
	return nil
}

func copyAndRemoveFile(src, dest string, openSource, createDest func(string) (*os.File, error)) error {
	in, err := openSource(src)
	if err != nil {
		return fmt.Errorf("trash: opening source %q: %w", src, err)
	}
	defer in.Close()

	out, err := createDest(dest)
	if err != nil {
		return fmt.Errorf("trash: creating dest %q: %w", dest, err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("trash: copying %q to %q: %w", src, dest, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("trash: closing dest %q: %w", dest, err)
	}
	if err := in.Close(); err != nil {
		return fmt.Errorf("trash: closing source %q: %w", src, err)
	}
	if err := os.Remove(src); err != nil {
		return fmt.Errorf("trash: removing source %q: %w", src, err)
	}
	return nil
}
