// Package security enforces the resource limits & path whitelist that make
// LogLens safe for AI agents to drive. All file access in LogLens goes
// through this package: nothing may be read from outside the configured
// whitelist directories, and every query is capped by line count, byte
// budget and timeout supplied via environment variables.
package security

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultMaxLines is the default LOGLENS_MAX_LINES.
	DefaultMaxLines = 2000
	// DefaultMaxBytes is the default LOGLENS_MAX_BYTES (10 MiB).
	DefaultMaxBytes int64 = 10 << 20
	// DefaultTimeout is the default LOGLENS_TIMEOUT_MS.
	DefaultTimeout = 15 * time.Second
)

// Limits holds the per-query resource caps.
type Limits struct {
	MaxLines int           // maximum lines returned by a single query
	MaxBytes int64         // maximum bytes read from disk by a single query
	Timeout  time.Duration // maximum wall-clock time for a single query
}

// IsZero reports whether no explicit limits were configured.
func (l Limits) IsZero() bool { return l == Limits{} }

// Config is the runtime security configuration of a LogLens server.
type Config struct {
	// Dirs are the allowed (cleaned, absolute) log directories.
	Dirs   []string
	Limits Limits

	once         sync.Once // guards dirsResolved
	dirsResolved []string  // symlink-resolved copy of Dirs
}

// LoadConfigFromEnv builds a Config from the LOGLENS_* environment variables.
// Missing variables fall back to safe defaults. An unset/empty whitelist is
// permitted (all access fails at query time) but is reported as a warning by
// the caller.
func LoadConfigFromEnv() (*Config, error) {
	cfg := &Config{Limits: Limits{
		MaxLines: DefaultMaxLines,
		MaxBytes: DefaultMaxBytes,
		Timeout:  DefaultTimeout,
	}}

	if v := strings.TrimSpace(os.Getenv("LOGLENS_WHITELIST_DIRS")); v != "" {
		for _, d := range strings.Split(v, ",") {
			d = strings.TrimSpace(d)
			if d == "" {
				continue
			}
			if !filepath.IsAbs(d) {
				abs, err := filepath.Abs(d)
				if err != nil {
					return nil, fmt.Errorf("LOGLENS_WHITELIST_DIRS: %w", err)
				}
				d = abs
			}
			cfg.Dirs = append(cfg.Dirs, filepath.Clean(d))
		}
	}

	if v := os.Getenv("LOGLENS_MAX_LINES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("LOGLENS_MAX_LINES must be a positive integer, got %q", v)
		}
		cfg.Limits.MaxLines = n
	}
	if v := os.Getenv("LOGLENS_MAX_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("LOGLENS_MAX_BYTES must be a positive integer, got %q", v)
		}
		cfg.Limits.MaxBytes = n
	}
	if v := os.Getenv("LOGLENS_TIMEOUT_MS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("LOGLENS_TIMEOUT_MS must be a positive integer, got %q", v)
		}
		cfg.Limits.Timeout = time.Duration(n) * time.Millisecond
	}
	return cfg, nil
}

// ExistingDirs returns the whitelist directories that currently exist on disk.
func (c *Config) ExistingDirs() []string {
	var out []string
	for _, d := range c.Dirs {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			out = append(out, d)
		}
	}
	return out
}

// HasDirs reports whether any whitelist directory is configured.
func (c *Config) HasDirs() bool { return len(c.Dirs) > 0 }
