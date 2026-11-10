package security

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestConfig(t *testing.T, dirs ...string) *Config {
	t.Helper()
	return &Config{Dirs: dirs}
}

func TestResolveAllowsPathsInsideWhitelist(t *testing.T) {
	base := t.TempDir()
	allowed := filepath.Join(base, "logs")
	if err := os.MkdirAll(filepath.Join(allowed, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := newTestConfig(t, allowed)

	cases := []string{
		allowed,
		filepath.Join(allowed, "app.log"),
		filepath.Join(allowed, "sub", "nested.log"),
	}
	for _, p := range cases {
		got, err := cfg.Resolve(p)
		if err != nil {
			t.Errorf("Resolve(%q) = %v", p, err)
			continue
		}
		if !IsWithinPath(got, allowed) {
			t.Errorf("Resolve(%q) = %q, outside %q", p, got, allowed)
		}
	}
}

func TestResolveRejectsTraversalAndOutside(t *testing.T) {
	base := t.TempDir()
	allowed := filepath.Join(base, "logs")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := newTestConfig(t, allowed)

	bad := []string{
		"/etc/passwd",
		filepath.Join(base, "elsewhere"),
		filepath.Join(allowed, "..", "..", "etc", "passwd"),
		filepath.Join(allowed, "..", "escape.log"),
		filepath.Join(allowed, "sub", "..", "..", "escape.log"),
	}
	for _, p := range bad {
		if _, err := cfg.Resolve(p); err == nil {
			t.Errorf("Resolve(%q) unexpectedly allowed", p)
		} else if !errors.Is(err, ErrNotAllowed) {
			t.Errorf("Resolve(%q) error = %v, want ErrNotAllowed", p, err)
		}
	}
}

func TestResolveRelativePathsAgainstCwd(t *testing.T) {
	cwd, _ := os.Getwd()
	base := t.TempDir()
	allowed := filepath.Join(base, "logs")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := newTestConfig(t, allowed)

	// ".." relative paths are anchored at cwd and never reach the temp dir.
	if _, err := cfg.Resolve("../../../etc/passwd"); err == nil {
		t.Error("relative traversal unexpectedly allowed")
	}
	_ = cwd
}

func TestResolveSymlinkEscape(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("symlink test skipped in CI")
	}
	base := t.TempDir()
	allowed := filepath.Join(base, "logs")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.log"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(allowed, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	cfg := newTestConfig(t, allowed)

	// Direct attempt through the symlinked dir.
	if _, err := cfg.Resolve(filepath.Join(link, "secret.log")); err == nil {
		t.Error("symlink escape allowed")
	}
	// A symlinked file inside allowed pointing outside.
	fileLink := filepath.Join(allowed, "linked.log")
	if err := os.Symlink(filepath.Join(outside, "secret.log"), fileLink); err == nil {
		if _, err := cfg.Resolve(fileLink); err == nil {
			t.Error("symlinked file escape allowed")
		}
	}
}

func TestWhitelistedSymlinkDirStillWorks(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("symlink test skipped in CI")
	}
	base := t.TempDir()
	target := filepath.Join(base, "real-logs")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "logs-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	cfg := newTestConfig(t, link)

	// The whitelist dir is itself a symlink; files inside resolve to the target
	// and must still be allowed.
	got, err := cfg.Resolve(filepath.Join(target, "app.log"))
	if err != nil {
		t.Fatalf("resolve through whitelisted symlink dir: %v", err)
	}
	if !strings.Contains(got, target) {
		t.Errorf("resolved path %q does not point at real target %q", got, target)
	}
	if _, err := cfg.Resolve(filepath.Join(link, "app.log")); err != nil {
		t.Errorf("resolve via symlink path: %v", err)
	}
}

func TestExistingDirs(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := newTestConfig(t, real, filepath.Join(base, "missing"))
	dirs := cfg.ExistingDirs()
	if len(dirs) != 1 || dirs[0] != real {
		t.Errorf("ExistingDirs() = %v, want [%q]", dirs, real)
	}
}
