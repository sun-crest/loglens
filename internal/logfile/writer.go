// Package logfile provides a small size-based rotating file writer for
// LogLens's own runtime log. Rotation is self-contained (no dependency on
// systemd/journald or an external logrotate): once a file would exceed its
// byte budget the oldest copy is dropped:
//
//	loglens.log  →  loglens.log.1  →  loglens.log.2  →  loglens.log.3
package logfile

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// DefaultKeep is how many rotated copies (.1, .2, …) are retained.
const DefaultKeep = 3

// Writer writes to path, rotating by size. It is safe for concurrent use.
type Writer struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	keep     int

	f    *os.File
	size int64
}

// New opens (creating if needed, appending otherwise) the log at path and
// returns a Writer that rotates beyond maxBytes, keeping keep rotated copies.
func New(path string, maxBytes int64) (*Writer, error) {
	w := &Writer{
		path:     path,
		maxBytes: maxBytes,
		keep:     DefaultKeep,
	}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

// Write appends p, rotating first if the budget would be exceeded. It returns
// the number of bytes accepted (an interrupted rotation counts as an error).
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotateLocked(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// Close closes the underlying file. The Writer must not be used afterwards.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

func (w *Writer) open() error {
	if err := os.MkdirAll(filepath.Dir(w.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.f = f
	w.size = st.Size()
	return nil
}

func (w *Writer) rotateLocked() error {
	if w.f != nil {
		if err := w.f.Close(); err != nil {
			return err
		}
		w.f = nil
	}
	// Shift existing backups outward, then renumber the live file to .1.
	for i := w.keep - 1; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", w.path, i)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := os.Rename(src, fmt.Sprintf("%s.%d", w.path, i+1)); err != nil {
			return err
		}
	}
	if _, err := os.Stat(w.path); err == nil {
		if err := os.Rename(w.path, w.path+".1"); err != nil {
			return err
		}
	}
	return w.open()
}