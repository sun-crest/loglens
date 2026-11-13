// Package config resolves LogLens runtime configuration from three sources:
// explicit CLI flags, an optional workspace conf file, and environment
// variables. Precedence for any given key is flag > conf file > env > default.
//
// The workspace ("directory tree") layout is the primary deployment shape:
//
//	<root>/
//	├── bin/loglens      # the static binary; root is inferred from its location
//	├── config/loglens.toml
//	└── logs/            # default whitelist target + optional runtime log
//
// Moving a whole <root> directory to another host works without touching any
// config, because the root is deduced from the binary's own path.
package config

import (
	"os"
	"path/filepath"
)

// ResolveRoot decides the workspace root: explicit flag first, then the
// LOGLENS_ROOT environment variable, then inference from the executable
// location. An empty result means classic mode: no directory tree, so all
// configuration comes from environment variables (the behaviour before the
// workspace layout existed).
func ResolveRoot(flagRoot string) string {
	if flagRoot != "" {
		return absClean(flagRoot)
	}
	if v := os.Getenv("LOGLENS_ROOT"); v != "" {
		return absClean(v)
	}
	if r, ok := inferRootFromExecutable(); ok {
		return r
	}
	return ""
}

// inferRootFromExecutable guesses the workspace root from the running binary:
// when the binary lives at <root>/bin/loglens and <root>/config exists, root is
// <root>. This is what makes a workspace tree relocatable.
func inferRootFromExecutable() (string, bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	return inferRootFromExePath(exe)
}

// inferRootFromExePath is the pure form of inferRootFromExecutable so it can
// be unit tested with arbitrary paths.
//
// A binary symlinked into, say, /usr/local/bin resolves to its real path, so
// the parent check naturally falls through to classic mode when there is no
// config/ sibling of the bin/ dir.
func inferRootFromExePath(exe string) (string, bool) {
	dir := filepath.Dir(exe)
	if filepath.Base(dir) != "bin" {
		return "", false
	}
	parent := filepath.Dir(dir)
	if st, err := os.Stat(filepath.Join(parent, "config")); err == nil && st.IsDir() {
		return parent, true
	}
	return "", false
}

// absClean returns p as an absolute, cleaned path.
func absClean(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return filepath.Clean(a)
}