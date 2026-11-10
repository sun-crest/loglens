// Package logreader implements lazy, budget-constrained reading of log files,
// including .gz rotated logs (streamed via compress/gzip, never decompressed
// fully to disk or memory).
package logreader

import (
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// maxLineBytes caps a single decoded line to keep memory bounded even for
// pathological records. The per-query byte budget normally intervenes first.
const maxLineBytes = 16 << 20

// ErrBudgetExceeded aborts iteration when the per-query byte budget runs out.
var ErrBudgetExceeded = errors.New("byte budget exceeded")

// IsGzip reports whether a filename looks like a gzip stream.
func IsGzip(p string) bool { return strings.HasSuffix(strings.ToLower(p), ".gz") }

// Line is a single decoded line (newline stripped).
type Line struct {
	Raw []byte
	No  int64 // 1-based line number
}

// OpenRead opens fpath for sequential reading, transparently decompressing gz
// files. The returned closer releases the gzip reader and underlying file.
func OpenRead(fpath string) (io.ReadCloser, error) {
	f, err := os.Open(fpath)
	if err != nil {
		return nil, err
	}
	if IsGzip(fpath) {
		gz, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("%s: %w", fpath, err)
		}
		return &multiCloser{gz, f}, nil
	}
	return f, nil
}

type multiCloser struct {
	io.ReadCloser
	f *os.File
}

func (mc *multiCloser) Close() error {
	e1 := mc.ReadCloser.Close()
	e2 := mc.f.Close()
	if e1 != nil {
		return e1
	}
	return e2
}

// Iterate streams decoded lines from r, calling emit for each. It stops at
// EOF, on an emit error, on ctx cancellation, or when the byte budget is
// exhausted (returning ErrBudgetExceeded). budget <= 0 disables the cap.
// The returned nBytes is the number of decoded bytes consumed; a single long
// line is flushed proportionally. Read-ahead buffering may slightly exceed
// the budget.
func Iterate(ctx context.Context, r io.Reader, budget int64, emit func(Line) error) (nLines, nBytes int64, err error) {
	br := bufio.NewReaderSize(r, 64<<10)
	var buf []byte
	for {
		if cerr := ctx.Err(); cerr != nil {
			return nLines, nBytes, cerr
		}
		chunk, chunkErr := br.ReadSlice('\n')
		if budget > 0 && nBytes+int64(len(chunk)) > budget {
			return nLines, nBytes, ErrBudgetExceeded
		}
		nBytes += int64(len(chunk))
		if len(buf)+len(chunk) > maxLineBytes {
			return nLines, nBytes, fmt.Errorf("line exceeds %d bytes", maxLineBytes)
		}
		buf = append(buf, chunk...)

		if chunkErr == bufio.ErrBufferFull {
			continue
		}
		if chunkErr != nil && chunkErr != io.EOF {
			return nLines, nBytes, chunkErr
		}
		if len(chunk) == 0 && chunkErr == io.EOF {
			// Clean EOF at chunk boundary: nothing pending.
			return nLines, nBytes, nil
		}
		nLines++
		if eerr := emit(Line{Raw: trimEOL(buf), No: nLines}); eerr != nil {
			return nLines, nBytes, eerr
		}
		buf = buf[:0]
		if chunkErr == io.EOF {
			return nLines, nBytes, nil
		}
	}
}

// trimEOL strips a trailing CR and/or LF and copies the buffer, so callers may
// hold onto it after the underlying bufio buffer is reused.
func trimEOL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
