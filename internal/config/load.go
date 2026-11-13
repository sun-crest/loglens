package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/sun-crest/loglens/internal/security"
)

const (
	// confFileName is the default configuration file inside the workspace.
	confFileName = "loglens.conf"

	// DefaultServeAddr is used when neither the conf file nor a flag sets it.
	DefaultServeAddr = "127.0.0.1:8787"

	// DefaultLogMaxBytes is the default runtime-log rotation threshold (10 MiB).
	DefaultLogMaxBytes int64 = 10 << 20
)

// Options carries the already-parsed explicit CLI overrides for Load.
type Options struct {
	// Root is the --root flag value ("" when not passed).
	Root string
}

// Set is the fully resolved runtime configuration of a LogLens process.
type Set struct {
	// Root is the workspace root ("" = classic mode, no directory tree).
	Root string

	// Config holds the whitelist directories and query limits.
	Config *security.Config

	// ServeAddr / ServeToken are the effective `serve` defaults after the
	// conf file has been applied (explicit flags still override in main).
	ServeAddr  string
	ServeToken string

	// LogFile is the absolute runtime-log path, "" = stderr.
	LogFile     string
	LogMaxBytes int64
}

// Load resolves the effective configuration. Precedence for every key:
// explicit CLI flag (via Options / main) > conf file > env > built-in default.
// With no workspace root and no conf file the result is byte-for-byte what the
// pre-workspace environment-only behaviour produced.
func Load(opts Options) (*Set, error) {
	// Base layer from env (which applies the built-in defaults).
	cfg, err := security.LoadConfigFromEnv()
	if err != nil {
		return nil, err
	}

	set := &Set{
		Root:        ResolveRoot(opts.Root),
		Config:      cfg,
		ServeAddr:   DefaultServeAddr,
		LogMaxBytes: DefaultLogMaxBytes,
	}

	// Optional conf file; keys present in it override env.
	var cf confFile
	if p := confPath(set.Root); p != "" {
		if _, err := toml.DecodeFile(p, &cf); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
	}

	// Dirs: conf > env > (workspace default: <root>/logs) / none.
	switch {
	case cf.Dirs != nil && cf.Dirs.Allow != nil:
		cfg.Dirs = expandDirs(*cf.Dirs.Allow, set.Root)
	case len(cfg.Dirs) == 0 && set.Root != "":
		cfg.Dirs = []string{filepath.Join(set.Root, "logs")}
	}

	// Limits: conf overrides the env/default layer.
	if cf.Limits != nil {
		if v := cf.Limits.MaxLines; v != nil {
			if *v <= 0 {
				return nil, fmt.Errorf("loglens.conf: limits.max_lines must be a positive integer, got %d", *v)
			}
			cfg.Limits.MaxLines = *v
		}
		if v := cf.Limits.MaxBytes; v != nil {
			if *v <= 0 {
				return nil, fmt.Errorf("loglens.conf: limits.max_bytes must be a positive integer, got %d", *v)
			}
			cfg.Limits.MaxBytes = *v
		}
		if v := cf.Limits.MaxFiles; v != nil {
			if *v <= 0 {
				return nil, fmt.Errorf("loglens.conf: limits.max_files must be a positive integer, got %d", *v)
			}
			cfg.Limits.MaxFiles = *v
		}
		if v := cf.Limits.TimeoutMs; v != nil {
			if *v <= 0 {
				return nil, fmt.Errorf("loglens.conf: limits.timeout_ms must be a positive integer, got %d", *v)
			}
			cfg.Limits.Timeout = time.Duration(*v) * time.Millisecond
		}
	}

	// Serve defaults from conf; main only applies explicit flags on top.
	if cf.Serve != nil {
		if cf.Serve.Addr != nil {
			set.ServeAddr = *cf.Serve.Addr
		}
		if cf.Serve.Token != nil {
			set.ServeToken = *cf.Serve.Token
		}
	}

	// Runtime log: conf > stderr (default is no file).
	if cf.Log != nil && cf.Log.File != nil {
		set.LogFile = resolveRel(*cf.Log.File, set.Root)
		if cf.Log.MaxSizeMB != nil && *cf.Log.MaxSizeMB > 0 {
			set.LogMaxBytes = int64(*cf.Log.MaxSizeMB) << 20
		}
	}

	return set, nil
}

// confPath returns the workspace config file path, or "" when there is no
// workspace or the file does not exist.
func confPath(root string) string {
	if root == "" {
		return ""
	}
	p := filepath.Join(root, "conf", confFileName)
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// expandDirs cleans every entry, anchors relative entries under root, and
// drops empty ones.
func expandDirs(dirs []string, root string) []string {
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if d = resolveRel(d, root); d != "" {
			out = append(out, d)
		}
	}
	return out
}

// resolveRel anchors a relative path under root. Without a workspace root a
// relative path resolves against the working directory. Empty input yields "".
func resolveRel(p, root string) string {
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	if root == "" {
		return absClean(p)
	}
	return filepath.Join(root, p)
}