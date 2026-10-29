package watcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watch represents a registered local root being watched. The sync engine
// can call Paths on it to obtain relative paths that have changed since
// the last call (debounced).
type Watch struct {
	id       string
	root     string
	watcher  *fsnotify.Watcher
	debounce time.Duration
	dirty    map[string]struct{}
	mu       sync.Mutex
	timer    *time.Timer
	cancel   context.CancelFunc
}

// Manager owns one fsnotify.Watcher and maps each registered folder-pair
// local root to a Watch.
type Manager struct {
	debounce time.Duration
	watches  map[string]*Watch
	mu       sync.Mutex
}

// NewManager returns a Manager that debounces events for the given duration.
func NewManager(debounce time.Duration) *Manager {
	return &Manager{debounce: debounce, watches: make(map[string]*Watch)}
}

// Add starts watching localPath for pairID. If localPath is already watched
// under a different pairID, the new pair is still tracked so both ids
// receive events.
func (m *Manager) Add(pairID, localPath string, onChange func(pairID string, paths []string)) (*Watch, error) {
	abs, err := filepath.Abs(localPath)
	if err != nil {
		return nil, fmt.Errorf("watcher: resolving %q: %w", localPath, err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if w, ok := m.watches[pairID]; ok {
		if w.root == abs {
			return w, nil
		}
		// Path changed: tear down old watch for this pair.
		_ = m.removeLocked(pairID)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("watcher: creating fsnotify watcher: %w", err)
	}

	if err := addRecursive(watcher, abs); err != nil {
		_ = watcher.Close()
		return nil, fmt.Errorf("watcher: adding recursive watch for %q: %w", abs, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := &Watch{
		id:       pairID,
		root:     abs,
		watcher:  watcher,
		debounce: m.debounce,
		dirty:    make(map[string]struct{}),
		cancel:   cancel,
	}
	m.watches[pairID] = w

	go w.run(ctx, onChange)
	return w, nil
}

// Remove stops watching localPath for pairID and removes the watch entry.
func (m *Manager) Remove(pairID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.removeLocked(pairID)
}

func (m *Manager) removeLocked(pairID string) error {
	w, ok := m.watches[pairID]
	if !ok {
		return nil
	}
	delete(m.watches, pairID)
	w.cancel()
	return w.watcher.Close()
}

// Close stops all watches.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var lastErr error
	for id := range m.watches {
		if err := m.removeLocked(id); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// addRecursive adds p and every nested directory to w.
func addRecursive(w *fsnotify.Watcher, p string) error {
	if err := w.Add(p); err != nil {
		return err
	}
	entries, err := filepath.Glob(filepath.Join(p, "*"))
	if err != nil {
		return err
	}
	for _, e := range entries {
		info, err := os.Stat(e)
		if err != nil || !info.IsDir() {
			continue
		}
		if err := addRecursive(w, e); err != nil {
			return err
		}
	}
	return nil
}

// Paths returns the currently dirty relative paths and clears the dirty
// set. It is safe for tests or the sync engine to call at any cadence;
// events arriving while a previous batch is being processed are batched.
func (w *Watch) Paths() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.dirty) == 0 {
		return nil
	}
	out := make([]string, 0, len(w.dirty))
	for p := range w.dirty {
		out = append(out, p)
	}
	w.dirty = make(map[string]struct{})
	return out
}

func (w *Watch) run(ctx context.Context, onChange func(pairID string, paths []string)) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			w.handleEvent(ev, onChange)
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			// fsnotify errors are non-fatal for v1; log via stdout.
			fmt.Fprintf(os.Stderr, "watcher error for %s: %v\n", w.id, err)
		}
	}
}

func (w *Watch) handleEvent(ev fsnotify.Event, onChange func(pairID string, paths []string)) {
	if ev.Op == 0 {
		return
	}
	if !isWatchedOp(ev.Op) {
		return
	}

	rel, err := filepath.Rel(w.root, ev.Name)
	if err != nil || strings.HasPrefix(rel, "..") {
		return
	}

	w.mu.Lock()
	w.dirty[rel] = struct{}{}
	if w.timer != nil {
		w.timer.Stop()
	}
	w.timer = time.AfterFunc(w.debounce, func() {
		paths := w.Paths()
		if len(paths) > 0 && onChange != nil {
			onChange(w.id, paths)
		}
	})
	w.mu.Unlock()

	// If a new directory was created, recurse into it so subsequent
	// changes inside it are also reported.
	if ev.Op.Has(fsnotify.Create) {
		if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
			_ = addRecursive(w.watcher, ev.Name)
		}
	}
}

func isWatchedOp(op fsnotify.Op) bool {
	return op.Has(fsnotify.Write) || op.Has(fsnotify.Create) || op.Has(fsnotify.Remove) || op.Has(fsnotify.Rename)
}