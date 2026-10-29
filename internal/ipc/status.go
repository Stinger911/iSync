package ipc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Status is a snapshot of the daemon's health and per-pair progress.
// It is written atomically to a status file the CLI reads.
type Status struct {
	UpdatedAt    time.Time           `json:"updated_at"`
	Healthy      bool                `json:"healthy"`
	Message      string              `json:"message,omitempty"`
	PairStatuses map[string]PairStatus `json:"pairs"`
}

// PairStatus captures one pair's most recent sync result.
type PairStatus struct {
	LastSyncAt time.Time `json:"last_sync_at,omitempty"`
	OK         bool      `json:"ok"`
	Error      string    `json:"error,omitempty"`
}

// StatusStore writes the daemon status snapshot atomically and re-reads it.
type StatusStore struct {
	path string
	mu   sync.Mutex
}

// NewStatusStore returns a store for the given status-file path.
func NewStatusStore(path string) *StatusStore {
	return &StatusStore{path: path}
}

// Write updates the status file atomically.
func (s *StatusStore) Write(status Status) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("ipc: creating status dir: %w", err)
	}

	raw, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return fmt.Errorf("ipc: encoding status: %w", err)
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("ipc: writing status temp: %w", err)
	}
	return os.Rename(tmp, s.path)
}

// Read returns the current status, or a zero Status if the file is missing.
func (s *StatusStore) Read() (Status, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return Status{}, nil
		}
		return Status{}, err
	}

	var status Status
	if err := json.Unmarshal(raw, &status); err != nil {
		return Status{}, fmt.Errorf("ipc: decoding status: %w", err)
	}
	return status, nil
}

// DefaultStatusPath returns the standard status file path under the user's
// runtime directory, falling back to ~/.local/share/isync/status.json.
func DefaultStatusPath() (string, error) {
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		return filepath.Join(runtimeDir, "isync", "status.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "isync", "status.json"), nil
}
