package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeConf writes a loglens.toml into root/config for the given root.
func writeConf(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, "loglens.toml")
	if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInferRootFromExePath(t *testing.T) {
	base := t.TempDir()

	// <base>/bin/loglens with <base>/config present -> root = base
	os.MkdirAll(filepath.Join(base, "bin"), 0o755)
	os.MkdirAll(filepath.Join(base, "config"), 0o755)
	if root, ok := inferRootFromExePath(filepath.Join(base, "bin", "loglens")); !ok || root != base {
		t.Fatalf("wanted root %q ok=true, got %q ok=%v", base, root, ok)
	}

	// binary in a dir not named bin -> no inference
	if _, ok := inferRootFromExePath(filepath.Join(base, "other", "loglens")); ok {
		t.Fatal("binary outside bin/ must not infer a root")
	}

	// bin/ without a config/ sibling -> no inference (classic mode)
	other := t.TempDir()
	os.MkdirAll(filepath.Join(other, "bin"), 0o755)
	if _, ok := inferRootFromExePath(filepath.Join(other, "bin", "loglens")); ok {
		t.Fatal("bin/ without config/ must not infer a root")
	}
}

func TestResolveRootFlagWinsOverEnv(t *testing.T) {
	t.Setenv("LOGLENS_ROOT", "/env/root")
	if r := ResolveRoot("/flag/root"); r != "/flag/root" {
		t.Fatalf("flag root should win, got %q", r)
	}
}

func TestResolveRootEnvWithoutFlag(t *testing.T) {
	t.Setenv("LOGLENS_ROOT", "/env/root")
	if r := ResolveRoot(""); r != "/env/root" {
		t.Fatalf("env root should apply without flag, got %q", r)
	}
}

// TestLoadConfOverridesEnv: a conf value beats the environment value.
func TestLoadConfOverridesEnv(t *testing.T) {
	t.Setenv("LOGLENS_MAX_LINES", "100")
	t.Setenv("LOGLENS_WHITELIST_DIRS", "/tmp")
	root := t.TempDir()
	writeConf(t, root, "[limits]\nmax_lines = 777\n")

	set, err := Load(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if got := set.Config.Limits.MaxLines; got != 777 {
		t.Fatalf("conf should override env, got %d", got)
	}
	if got := set.Config.Dirs[0]; got != "/tmp" {
		t.Fatalf("whitelist env entry should survive when conf omits dirs, got %v", set.Config.Dirs)
	}
}

// TestLoadEnvWhenConfOmitsKey: an absent conf key falls back to env.
func TestLoadEnvWhenConfOmitsKey(t *testing.T) {
	t.Setenv("LOGLENS_MAX_LINES", "100")
	t.Setenv("LOGLENS_WHITELIST_DIRS", "/tmp")
	root := t.TempDir()
	writeConf(t, root, "[lim]\n") // no valid key, only an unrelated table

	set, err := Load(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if got := set.Config.Limits.MaxLines; got != 100 {
		t.Fatalf("env should apply when conf omits the key, got %d", got)
	}
}

// TestLoadWorkspaceDefaultDirs: without conf and env, a workspace defaults to
// reading from <root>/logs.
func TestLoadWorkspaceDefaultDirs(t *testing.T) {
	t.Setenv("LOGLENS_WHITELIST_DIRS", "")
	root := t.TempDir()
	set, err := Load(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "logs")
	if len(set.Config.Dirs) != 1 || set.Config.Dirs[0] != want {
		t.Fatalf("workspace should default to <root>/logs, got %v", set.Config.Dirs)
	}
}

// TestLoadConfDirsRelative: conf entries resolve relative to the root.
func TestLoadConfDirsRelative(t *testing.T) {
	t.Setenv("LOGLENS_WHITELIST_DIRS", "/elsewhere")
	root := t.TempDir()
	writeConf(t, root, "[dirs]\nallow = [\"logs\", \"/var/log\"]\n")

	set, err := Load(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(root, "logs"), "/var/log"}
	if len(set.Config.Dirs) != 2 || set.Config.Dirs[0] != want[0] || set.Config.Dirs[1] != want[1] {
		t.Fatalf("conf dirs mismatch, got %v want %v", set.Config.Dirs, want)
	}
}

// TestLoadDefaults: no env, no conf -> built-in defaults, classic mode.
func TestLoadDefaults(t *testing.T) {
	t.Setenv("LOGLENS_WHITELIST_DIRS", "")
	t.Setenv("LOGLENS_MAX_LINES", "")
	t.Setenv("LOGLENS_MAX_BYTES", "")
	t.Setenv("LOGLENS_MAX_FILES", "")
	t.Setenv("LOGLENS_TIMEOUT_MS", "")
	set, err := Load(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Config.Dirs) != 0 {
		t.Fatalf("classic mode with no whitelist should have empty dirs, got %v", set.Config.Dirs)
	}
	l := set.Config.Limits
	if l.MaxLines != 2000 || l.MaxBytes != 10<<20 || l.MaxFiles != 5000 || l.Timeout.String() != "15s" {
		t.Fatalf("unexpected defaults: %+v", l)
	}
	if set.ServeAddr != DefaultServeAddr {
		t.Fatalf("serve addr should default to %q, got %q", DefaultServeAddr, set.ServeAddr)
	}
}

// TestLoadServeDefaultsFromConf: addr/token come from the conf file.
func TestLoadServeDefaultsFromConf(t *testing.T) {
	root := t.TempDir()
	writeConf(t, root, "[serve]\naddr = \"0.0.0.0:9999\"\ntoken = \"sekret\"\n")
	set, err := Load(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if set.ServeAddr != "0.0.0.0:9999" || set.ServeToken != "sekret" {
		t.Fatalf("serve defaults from conf, got addr=%q token=%q", set.ServeAddr, set.ServeToken)
	}
}

// TestLoadLogFileRelative: log.file resolves against the root.
func TestLoadLogFileRelative(t *testing.T) {
	root := t.TempDir()
	writeConf(t, root, "[log]\nfile = \"logs/loglens.log\"\nmax_size_mb = 5\n")
	set, err := Load(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "logs", "loglens.log"); set.LogFile != want {
		t.Fatalf("log.file should anchor under root, got %q", set.LogFile)
	}
	if set.LogMaxBytes != 5<<20 {
		t.Fatalf("max_size_mb should scale to bytes, got %d", set.LogMaxBytes)
	}
}

func TestLoadInvalidLimits(t *testing.T) {
	root := t.TempDir()
	writeConf(t, root, "[limits]\nmax_lines = 0\n")
	if _, err := Load(Options{Root: root}); err == nil {
		t.Fatal("max_lines = 0 should be rejected")
	}
}

func TestLoadInvalidTOML(t *testing.T) {
	root := t.TempDir()
	writeConf(t, root, "[limits\nmax_lines = nope")
	if _, err := Load(Options{Root: root}); err == nil {
		t.Fatal("malformed TOML should be rejected")
	}
}

// TestLoadClassicModeIgnoresConf: with no root there is no conf lookup, so
// configuration is purely from env.
func TestLoadClassicModeIgnoresConf(t *testing.T) {
	t.Setenv("LOGLENS_WHITELIST_DIRS", "/tmp")
	wd := t.TempDir()
	writeConf(t, wd, "[limits]\nmax_lines = 42\n")
	t.Chdir(wd) // the conf tree exists, but classic mode must not read it
	set, err := Load(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if set.Config.Limits.MaxLines != 2000 {
		t.Fatalf("classic mode must ignore a stray conf tree, got %d", set.Config.Limits.MaxLines)
	}
	if len(set.Config.Dirs) != 1 || set.Config.Dirs[0] != "/tmp" {
		t.Fatalf("classic mode dirs from env, got %v", set.Config.Dirs)
	}
}