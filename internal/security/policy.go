package security

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrNotAllowed is returned when a path falls outside the whitelist, or cannot
// be resolved (which usually means path traversal or a nonexistent path).
var ErrNotAllowed = errors.New("path is outside the allowed log directories")

// resolveExisting returns the canonical (symlink-resolved) absolute path for
// path. The path itself need not exist: the deepest existing ancestor is
// resolved and the remaining suffix re-attached. This defeats symlink escapes
// (including inside the whitelist) while still resolving paths that point at
// yet-to-be-created or opened-later files.
func resolveExisting(path string) (string, error) {
	abs := path
	if !filepath.IsAbs(abs) {
		a, err := filepath.Abs(abs)
		if err != nil {
			return "", err
		}
		abs = a
	}
	abs = filepath.Clean(abs)

	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved), nil
	}

	// Walk up to the deepest existing ancestor.
	dir := filepath.Dir(abs)
	for dir != "." && dir != string(filepath.Separator) {
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			rel, err := filepath.Rel(dir, abs)
			if err != nil {
				return "", err
			}
			return filepath.Join(r, rel), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", errors.New("path cannot be resolved")
}

// IsWithinPath reports whether child equals base or lives underneath base.
func IsWithinPath(child, base string) bool {
	if child == base {
		return true
	}
	rel, err := filepath.Rel(base, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Resolve canonicalizes request and verifies it is inside a whitelist
// directory (following symlinks both ways). The returned path is the
// canonical absolute path that should be used for actual I/O.
func (c *Config) Resolve(request string) (string, error) {
	abs := request
	if !filepath.IsAbs(abs) {
		a, err := filepath.Abs(filepath.Clean(abs))
		if err != nil {
			return "", fmt.Errorf("%w: bad path %q: %v", ErrNotAllowed, request, err)
		}
		abs = a
	} else {
		abs = filepath.Clean(abs)
	}

	resolved, err := resolveExisting(abs)
	if err != nil {
		return "", fmt.Errorf("%w: cannot resolve %q", ErrNotAllowed, request)
	}

	for _, dir := range c.resolvedDirs() {
		if IsWithinPath(resolved, dir) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("%w: %q is not under any LOGLENS_WHITELIST_DIRS (%s)",
		ErrNotAllowed, request, strings.Join(c.Dirs, ", "))
}

// resolvedDirs returns the symlink-resolved whitelist directories, cached per
// Config. Directories that cannot be resolved are kept as-is.
func (c *Config) resolvedDirs() []string {
	c.once.Do(func() {
		c.dirsResolved = make([]string, 0, len(c.Dirs))
		for _, d := range c.Dirs {
			if r, err := resolveExisting(d); err == nil {
				c.dirsResolved = append(c.dirsResolved, r)
			} else {
				c.dirsResolved = append(c.dirsResolved, d)
			}
		}
	})
	return c.dirsResolved
}
