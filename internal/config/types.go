package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Duration wraps time.Duration so it can be read from/written to TOML as a
// human string like "15s" or "5m", matching spec.md's example config.
type Duration time.Duration

// UnmarshalText implements encoding.TextUnmarshaler, used by TOML decoders
// that support it (e.g. pelletier/go-toml/v2) for string-typed values.
func (d *Duration) UnmarshalText(text []byte) error {
	parsed, err := time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("config: invalid duration %q: %w", string(text), err)
	}
	*d = Duration(parsed)
	return nil
}

// MarshalText implements encoding.TextMarshaler.
func (d Duration) MarshalText() ([]byte, error) {
	return []byte(time.Duration(d).String()), nil
}

// Std returns the value as a standard library time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// Size wraps a byte count so it can be read from/written to TOML as a
// human string like "10GiB" or "500MB", matching spec.md's example config.
type Size int64

var sizeUnits = []struct {
	suffix string
	factor int64
}{
	// Longest suffixes first so matching doesn't stop early (e.g. "GiB"
	// before "G", "KiB" before "K").
	{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10},
	{"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"KB", 1e3},
	{"T", 1 << 40}, {"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10},
	{"B", 1},
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (s *Size) UnmarshalText(text []byte) error {
	str := strings.TrimSpace(string(text))
	for _, u := range sizeUnits {
		if strings.HasSuffix(str, u.suffix) {
			numPart := strings.TrimSpace(strings.TrimSuffix(str, u.suffix))
			if numPart == "" {
				return fmt.Errorf("config: invalid size %q", str)
			}
			val, err := strconv.ParseFloat(numPart, 64)
			if err != nil {
				return fmt.Errorf("config: invalid size %q: %w", str, err)
			}
			*s = Size(val * float64(u.factor))
			return nil
		}
	}
	// Bare number, assume bytes.
	val, err := strconv.ParseInt(str, 10, 64)
	if err != nil {
		return fmt.Errorf("config: invalid size %q: %w", str, err)
	}
	*s = Size(val)
	return nil
}

// MarshalText implements encoding.TextMarshaler, rendering as GiB when the
// value divides evenly, otherwise as a plain byte count.
func (s Size) MarshalText() ([]byte, error) {
	const gib = 1 << 30
	if int64(s)%gib == 0 {
		return []byte(fmt.Sprintf("%dGiB", int64(s)/gib)), nil
	}
	return []byte(strconv.FormatInt(int64(s), 10)), nil
}

// Bytes returns the value as a plain int64 byte count.
func (s Size) Bytes() int64 { return int64(s) }
