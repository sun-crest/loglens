package logreader

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FileInfo describes a discovered log file.
type FileInfo struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	ModTime    string `json:"mod_time"` // RFC3339
	Compressed bool   `json:"compressed"`
}

// ListOptions controls file discovery.
type ListOptions struct {
	Root      string // absolute, already-whitelisted directory
	Glob      string // filepath.Match pattern; with '/' matches the path relative to Root
	Recursive bool
	MaxFiles  int  // 0 = unlimited
	Truncated *bool // if non-nil, set to true when MaxFiles cut the walk short
}

// ListFiles walks Root and returns regular files matching options, sorted by
// path. Symlinks are not followed (paths are resolved at read time against the
// whitelist); the directory walk itself never escapes Root.
func ListFiles(root string, opts ListOptions) ([]FileInfo, error) {
	var out []FileInfo
	seen := make(map[string]bool)
	count := 0
	tooMany := func() bool { return opts.MaxFiles > 0 && count >= opts.MaxFiles }

	add := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if tooMany() {
			if opts.Truncated != nil {
				*opts.Truncated = true
			}
			return filepath.SkipAll
		}
		// Do not follow symlinks in listings; paths are re-resolved at read time.
		if d.Type()&fs.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		fi, lerr := os.Stat(path)
		if lerr != nil || !fi.Mode().IsRegular() {
			return nil
		}
		if !matchGlob(path, root, opts.Glob) {
			return nil
		}
		if seen[path] {
			return nil
		}
		seen[path] = true
		out = append(out, FileInfo{
			Path:       path,
			Size:       fi.Size(),
			ModTime:    fi.ModTime().UTC().Format("2006-01-02T15:04:05Z07:00"),
			Compressed: IsGzip(path),
		})
		count++
		return nil
	}

	err := filepath.WalkDir(root, add)
	if err == filepath.SkipAll {
		err = nil // hit MaxFiles: stop cleanly
	}
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// matchGlob matches pattern against path's base name, or against its relative
// path to root when the pattern contains a separator.
func matchGlob(path, root, pattern string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	base := filepath.Base(path)
	if strings.Contains(pattern, "/") || strings.Contains(pattern, "\\") {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return false
		}
		ok, _ := filepath.Match(pattern, rel)
		return ok
	}
	ok, _ := filepath.Match(pattern, base)
	return ok
}
