package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Load reads and validates the config file at path, merging any absent
// daemon settings with Default()'s values. A missing file is not an
// error — Default() with no pairs is returned instead, so a first run
// before `isync add` has ever been called still works.
func Load(path string) (Config, error) {
	cfg := Default()

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("config: reading %s: %w", path, err)
	}

	// Decode onto a copy that already has defaults applied, so fields
	// absent from the file keep their default value rather than zeroing.
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("config: parsing %s: %w", path, err)
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("config: %s: %w", path, err)
	}

	return cfg, nil
}

// Save writes cfg to path as TOML, creating parent directories as needed.
func Save(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("config: creating config dir: %w", err)
	}

	data, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("config: encoding: %w", err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("config: writing %s: %w", path, err)
	}

	return nil
}

// Validate checks structural/semantic constraints that TOML parsing alone
// can't enforce: required fields, duplicate pair IDs, positive sizes, etc.
func (c Config) Validate() error {
	if c.Daemon.MaxConcurrentXfers <= 0 {
		return fmt.Errorf("daemon.max_concurrent_transfers must be positive, got %d", c.Daemon.MaxConcurrentXfers)
	}
	if c.Daemon.MaxFileSize <= 0 {
		return fmt.Errorf("daemon.max_file_size must be positive, got %d", c.Daemon.MaxFileSize)
	}
	if c.Daemon.TrashRetentionDays < 0 {
		return fmt.Errorf("daemon.trash_retention_days must not be negative, got %d", c.Daemon.TrashRetentionDays)
	}
	if c.Daemon.PollIntervalBaseline.Std() <= 0 ||
		c.Daemon.PollIntervalActive.Std() <= 0 ||
		c.Daemon.PollIntervalIdle.Std() <= 0 {
		return fmt.Errorf("daemon poll intervals must all be positive")
	}

	seen := make(map[string]struct{}, len(c.Pairs))
	for _, p := range c.Pairs {
		if strings.TrimSpace(p.ID) == "" {
			return fmt.Errorf("pair with empty id (icloud_path=%q, local_path=%q)", p.ICloudPath, p.LocalPath)
		}
		if _, dup := seen[p.ID]; dup {
			return fmt.Errorf("duplicate pair id %q", p.ID)
		}
		seen[p.ID] = struct{}{}

		if strings.TrimSpace(p.ICloudPath) == "" {
			return fmt.Errorf("pair %q: icloud_path must not be empty", p.ID)
		}
		if strings.TrimSpace(p.LocalPath) == "" {
			return fmt.Errorf("pair %q: local_path must not be empty", p.ID)
		}
		if !filepath.IsAbs(p.LocalPath) {
			return fmt.Errorf("pair %q: local_path must be absolute, got %q", p.ID, p.LocalPath)
		}
	}

	return nil
}
