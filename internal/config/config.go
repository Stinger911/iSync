// Package config loads and validates iSync's TOML configuration file,
// as specified in spec.md §2.3.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Defaults for daemon settings, applied when a value is absent from the
// config file (per spec.md and the resolved INTENT.md decisions).
const (
	DefaultPollIntervalBaseline = 60 * time.Second
	DefaultPollIntervalActive   = 15 * time.Second
	DefaultPollIntervalIdle     = 5 * time.Minute
	DefaultMaxConcurrentXfers   = 3
	DefaultMaxFileSize          = 10 * 1024 * 1024 * 1024 // 10 GiB
	DefaultTrashRetentionDays   = 30
)

// Daemon holds background-service tuning settings.
type Daemon struct {
	PollIntervalIdle     Duration `toml:"poll_interval_idle"`
	PollIntervalActive   Duration `toml:"poll_interval_active"`
	PollIntervalBaseline Duration `toml:"poll_interval_baseline"`
	MaxConcurrentXfers   int      `toml:"max_concurrent_transfers"`
	MaxFileSize          Size     `toml:"max_file_size"`
	TrashRetentionDays   int      `toml:"trash_retention_days"`
}

// Pair is one registered iCloud Drive <-> local folder sync pair.
type Pair struct {
	ID         string `toml:"id"`
	ICloudPath string `toml:"icloud_path"`
	LocalPath  string `toml:"local_path"`
	Enabled    bool   `toml:"enabled"`
	RemotePath string `toml:"remote_path"`
}

// Config is the root TOML document shape.
type Config struct {
	Daemon Daemon `toml:"daemon"`
	Pairs  []Pair `toml:"pairs"`
}

// DefaultPath returns the conventional config file location,
// ~/.config/isync/config.toml, honoring $XDG_CONFIG_HOME.
func DefaultPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("config: resolving home directory: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "isync", "config.toml"), nil
}

// CredentialPath returns the encrypted credential file path,
// ~/.config/isync/credentials.age.
func CredentialPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("config: resolving home directory: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "isync", "credentials.age"), nil
}

// StatePath returns the BoltDB state database path,
// ~/.local/share/isync/state.db.
func StatePath() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("config: resolving home directory: %w", err)
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "isync", "state.db"), nil
}

// TrashPath returns the quarantine root directory,
// ~/.local/share/isync/trash.
func TrashPath() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("config: resolving home directory: %w", err)
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "isync", "trash"), nil
}

// Default returns a Config populated with all documented defaults and no
// registered pairs — the starting point before a file is loaded/merged.
func Default() Config {
	return Config{
		Daemon: Daemon{
			PollIntervalIdle:     Duration(DefaultPollIntervalIdle),
			PollIntervalActive:   Duration(DefaultPollIntervalActive),
			PollIntervalBaseline: Duration(DefaultPollIntervalBaseline),
			MaxConcurrentXfers:   DefaultMaxConcurrentXfers,
			MaxFileSize:          Size(DefaultMaxFileSize),
			TrashRetentionDays:   DefaultTrashRetentionDays,
		},
	}
}
