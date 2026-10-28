package config

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLoad_MissingFile_ReturnsDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("Load() error = %v, want nil for missing file", err)
	}
	if cfg.Daemon.MaxConcurrentXfers != DefaultMaxConcurrentXfers {
		t.Errorf("MaxConcurrentXfers = %d, want default %d", cfg.Daemon.MaxConcurrentXfers, DefaultMaxConcurrentXfers)
	}
	if len(cfg.Pairs) != 0 {
		t.Errorf("Pairs = %v, want empty on first run", cfg.Pairs)
	}
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	cfg := Default()
	cfg.Pairs = []Pair{
		{ID: "documents", ICloudPath: "/Documents", LocalPath: "/home/user/iCloudDocuments"},
	}
	cfg.Daemon.PollIntervalActive = Duration(5 * time.Second)
	cfg.Daemon.MaxFileSize = Size(20 * 1024 * 1024 * 1024)

	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if len(got.Pairs) != 1 || got.Pairs[0].ID != "documents" {
		t.Errorf("Pairs = %+v, want one pair id=documents", got.Pairs)
	}
	if got.Daemon.PollIntervalActive.Std() != 5*time.Second {
		t.Errorf("PollIntervalActive = %v, want 5s", got.Daemon.PollIntervalActive.Std())
	}
	if got.Daemon.MaxFileSize.Bytes() != 20*1024*1024*1024 {
		t.Errorf("MaxFileSize = %d, want 20GiB", got.Daemon.MaxFileSize.Bytes())
	}
}

func TestValidate_RejectsDuplicatePairIDs(t *testing.T) {
	cfg := Default()
	cfg.Pairs = []Pair{
		{ID: "dup", ICloudPath: "/A", LocalPath: "/home/user/a"},
		{ID: "dup", ICloudPath: "/B", LocalPath: "/home/user/b"},
	}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() = nil, want error for duplicate pair id")
	}
}

func TestValidate_RejectsRelativeLocalPath(t *testing.T) {
	cfg := Default()
	cfg.Pairs = []Pair{{ID: "x", ICloudPath: "/A", LocalPath: "relative/path"}}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() = nil, want error for relative local_path")
	}
}

func TestValidate_RejectsNonPositiveSettings(t *testing.T) {
	cfg := Default()
	cfg.Daemon.MaxConcurrentXfers = 0
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() = nil, want error for zero MaxConcurrentXfers")
	}
}

func TestSizeUnmarshal(t *testing.T) {
	cases := map[string]int64{
		"10GiB": 10 * (1 << 30),
		"500MB": 500 * 1_000_000,
		"1024":  1024,
		"2KiB":  2 * (1 << 10),
	}
	for in, want := range cases {
		var s Size
		if err := s.UnmarshalText([]byte(in)); err != nil {
			t.Errorf("UnmarshalText(%q) error = %v", in, err)
			continue
		}
		if s.Bytes() != want {
			t.Errorf("UnmarshalText(%q) = %d, want %d", in, s.Bytes(), want)
		}
	}
}
