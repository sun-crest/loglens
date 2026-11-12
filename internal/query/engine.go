// Package query implements the log slicing & search engine that backs the MCP
// tools. Everything funnels resource enforcement (line limits, byte budget,
// timeouts) set by the security package.
package query

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sun-crest/loglens/internal/logreader"
	"github.com/sun-crest/loglens/internal/parser"
	"github.com/sun-crest/loglens/internal/security"
)

// Entry is a single returned log line (a hit or a context line).
type Entry struct {
	File string  `json:"file"`
	Line int64   `json:"line"`
	Time *string `json:"time,omitempty"` // RFC3339Nano (UTC) when parsed
	Text string  `json:"text"`
}

// Result is what a slice/search returns to the MCP caller.
type Result struct {
	Files         []string `json:"files"`         // files that were actually scanned
	Entries       []Entry  `json:"entries"`       // hit + context lines, in read order
	TotalMatched  int      `json:"total_matched"` // lines matching the filters (excludes context)
	ScannedBytes  int64    `json:"scanned_bytes"`
	UnparsedLines int64    `json:"unparsed_lines,omitempty"` // lines without a parseable timestamp in a time-bounded query
	Truncated     bool     `json:"truncated"`                // true if a limit cut results short
	Reason        string   `json:"reason,omitempty"`
}

// Options configures a single query run.
type Options struct {
	Files         []string
	Policy        *security.Config
	Limits        security.Limits
	HasBounds     bool
	Start         time.Time
	End           time.Time
	Keywords      []string
	Exclude       []string
	IgnoreCase    bool
	UseRegex      bool
	ContextBefore int
	ContextAfter  int
	LimitLines    int // 0 => Limits.MaxLines
	SortByTime    bool
	// FullScan forces the full-file path even without time bounds. Used by
	// search_logs, whose no-bounds + keywords contract is a whole-|file| search;
	// get_log_slice without bounds keeps the cheap tail semantics instead.
	FullScan bool
}

// tailMode reports whether the query reads only the tail window of the newest
// file: no time bounds, no context lines. Keywords/exclude filters are allowed
// and are applied inside the tail window (returns the most recent matching
// lines). FullScan suppresses tail so search stays whole-file.
func (o *Options) tailMode() bool {
	if o.FullScan {
		return false
	}
	return !o.HasBounds && o.ContextBefore == 0 && o.ContextAfter == 0 && len(o.Files) > 0
}

// errResultFull aborts iteration once the returned-line cap is hit.
var errResultFull = errors.New("result limit reached")

// errWindowPassed stops a seeked time-slice once all remaining lines are known
// to be after the window end (log timestamps are monotonic in practice). It is
// an optimization for seeked queries, not a data-loss flag.
var errWindowPassed = errors.New("window passed")

// Run executes a query. Resource exhaustion (timeout or byte budget) returns a
// partial Result with Truncated set, not an error, so the MCP caller can see
// how far it got. Path errors and bad regexes are returned as errors.
func Run(ctx context.Context, opts Options) (*Result, error) {
	if opts.Policy == nil {
		return nil, errors.New("query: nil policy")
	}
	if opts.LimitLines <= 0 || opts.LimitLines > opts.Limits.MaxLines {
		opts.LimitLines = opts.Limits.MaxLines
	}
	if opts.LimitLines <= 0 {
		opts.LimitLines = security.DefaultMaxLines
	}
	m, err := newMatcher(&opts)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, opts.Limits.Timeout)
	defer cancel()
	now := time.Now()

	res := &Result{Entries: []Entry{}}

	// No time bounds, no context: cheap tail of the newest file. Keywords /
	// excludes (if any) are applied inside the tail window, returning the most
	// recent matching lines.
	if opts.tailMode() {
		return runTail(ctx, opts, m, res, now)
	}

	for _, f := range opts.Files {
		if res.Truncated || ctx.Err() != nil {
			break
		}
		if err := runFile(ctx, f, &opts, m, res, now); err != nil {
			return nil, err
		}
	}

	if opts.SortByTime && opts.HasBounds && opts.ContextBefore == 0 && opts.ContextAfter == 0 {
		sortEntriesByTime(res.Entries)
	}
	return res, nil
}

// locateBackoffBytes is how far before the located start we begin scanning, so
// lines without a parseable timestamp (or a small probing error) right at the
// boundary are not missed; the time-window filter removes anything before Start.
const locateBackoffBytes = 32 << 10 // 32 KiB

// locateStartOffset finds, for a plain (non-gz) file, a byte offset from which
// sequential scanning will reach lines with timestamp >= start without reading
// the whole file head. A binary search probes mid-file lines; the returned
// offset backs off locateBackoffBytes before the located boundary. The number
// of bytes consumed by probing is returned for budget accounting. Returns 0
// (scan from head) when the file has no probeable timestamp.
func locateStartOffset(f *os.File, size int64, start time.Time, now time.Time) (off, probeBytes int64) {
	if size <= 0 {
		return 0, 0
	}
	lo, hi := int64(0), size-1
	for lo <= hi {
		mid := lo + (hi-lo)/2
		t, ok, n := probeTimeAt(f, mid, now)
		probeBytes += n
		if !ok {
			// Offset is mid-line or the file has no parseable timestamps:
			// fall back to scanning from the head to stay correct.
			return 0, probeBytes
		}
		if t.Before(start) {
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	best := lo
	if best > locateBackoffBytes {
		best -= locateBackoffBytes
	} else {
		best = 0
	}
	return best, probeBytes
}

// probeTimeAt seeks to off and returns the timestamp of the first whole line
// at or after that offset (the first read may start mid-line and is skipped).
// n is the number of bytes read during probing.
func probeTimeAt(f *os.File, off int64, now time.Time) (time.Time, bool, int64) {
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return time.Time{}, false, 0
	}
	br := bufio.NewReaderSize(f, 64<<10)
	// Discard the possibly mid-line first read, then inspect whole lines.
	var n int64
	for i := 0; i < 4; i++ {
		line, err := br.ReadSlice('\n')
		n += int64(len(line))
		if i == 0 {
			// This chunk may start inside a line (bogus leading timestamp):
			// skip it unless we are at offset 0 and got a complete first line.
			if err == nil && off == 0 {
				if t, ok := parser.ExtractLineTime(line, now); ok {
					return t, true, n
				}
			}
			if err == nil || err == bufio.ErrBufferFull {
				if off == 0 {
					return time.Time{}, false, n // offset 0: examine as-is below
				}
				continue
			}
			return time.Time{}, false, n
		}
		if t, ok := parser.ExtractLineTime(line, now); ok {
			return t, true, n
		}
		if err != nil && err != bufio.ErrBufferFull {
			return time.Time{}, false, n
		}
	}
	return time.Time{}, false, n
}

// runFile evaluates one file: resolve against the policy (defense in depth),
// open lazily (gz-aware), then stream lines through the filter pipeline.
func runFile(ctx context.Context, f string, opts *Options, m *matcher, res *Result, now time.Time) error {
	resolved, err := opts.Policy.Resolve(f)
	if err != nil {
		return err
	}

	// Plain file with a start bound: seek toward the window instead of scanning
	// the whole head. gz files cannot seek and fall through to the full scan.
	if opts.HasBounds && !opts.Start.IsZero() && !logreader.IsGzip(resolved) {
		fh, oerr := os.Open(resolved)
		if oerr != nil {
			return oerr
		}
		defer fh.Close()
		res.Files = append(res.Files, resolved)
		if st, serr := fh.Stat(); serr == nil {
			off, probe := locateStartOffset(fh, st.Size(), opts.Start, now)
			res.ScannedBytes += probe
			if _, serr := fh.Seek(off, io.SeekStart); serr != nil {
				return serr
			}
			return scanReader(ctx, fh, resolved, opts, m, res, now)
		}
	}

	rc, err := logreader.OpenRead(resolved)
	if err != nil {
		return err
	}
	defer rc.Close()
	res.Files = append(res.Files, resolved)
	return scanReader(ctx, rc, resolved, opts, m, res, now)
}

// scanReader streams rc through the filter pipeline sharing the runFile logic.
func scanReader(ctx context.Context, rc io.Reader, resolved string, opts *Options, m *matcher, res *Result, now time.Time) error {
	budget := opts.Limits.MaxBytes - res.ScannedBytes
	if budget <= 0 {
		res.Truncated = true
		res.Reason = "byte budget (LOGLENS_MAX_BYTES) exceeded"
		return nil
	}

	var (
		before         []Entry
		afterRemaining int
	)
	_, nb, scanErr := logreader.Iterate(ctx, rc, budget, func(l logreader.Line) error {
		lineStr := string(l.Raw)

		var entryTime *string
		if opts.HasBounds {
			tt, ok := parser.ExtractLineTime(l.Raw, now)
			if !ok {
				res.UnparsedLines++
				return nil // cannot verify a timestamp for a time-sliced query: skip
			}
			if tt.Before(opts.Start) || tt.After(opts.End) {
				// Past the window end (timestamps are monotonic in practice):
				// stop early when no context_after is still owed, so a seeked
				// query does not burn the byte budget on lines after the window.
				if tt.After(opts.End) && afterRemaining == 0 {
					return errWindowPassed
				}
				return nil
			}
			s := tt.UTC().Format(time.RFC3339Nano)
			entryTime = &s
		} else if tt, ok := parser.ExtractLineTime(l.Raw, now); ok {
			// No bounds, but surface the timestamp when we can read one.
			s := tt.UTC().Format(time.RFC3339Nano)
			entryTime = &s
		}

		entry := Entry{File: resolved, Line: l.No, Time: entryTime, Text: lineStr}

		if !m.matches(lineStr) {
			// Filtered out: keep as context (before or after) when requested.
			if afterRemaining > 0 {
				if !appendEntry(res, opts, entry) {
					return errResultFull
				}
				afterRemaining--
			} else if opts.ContextBefore > 0 {
				before = pushContext(before, entry, opts.ContextBefore)
			}
			return nil
		}

		// Hit: flush pending context-before lines, then the hit.
		for _, e := range before {
			if !appendEntry(res, opts, e) {
				return errResultFull
			}
		}
		before = before[:0]
		res.TotalMatched++
		if !appendEntry(res, opts, entry) {
			return errResultFull
		}
		afterRemaining = opts.ContextAfter
		return nil
	})
	res.ScannedBytes += nb

	switch {
	case scanErr == nil || errors.Is(scanErr, errWindowPassed):
		return nil
	case errors.Is(scanErr, errResultFull):
		res.Truncated = true
		res.Reason = "line limit reached"
		return nil
	case errors.Is(scanErr, logreader.ErrBudgetExceeded):
		res.Truncated = true
		res.Reason = "byte budget (LOGLENS_MAX_BYTES) exceeded"
		return nil
	case errors.Is(scanErr, context.DeadlineExceeded), errors.Is(scanErr, context.Canceled):
		res.Truncated = true
		res.Reason = "query timeout reached"
		return nil
	default:
		return scanErr
	}
}

// pushContext appends e to a bounded context-before buffer, dropping the
// oldest line when the buffer overflows.
func pushContext(buf []Entry, e Entry, cap int) []Entry {
	if len(buf) >= cap {
		buf = buf[1:]
	}
	return append(buf, e)
}

// appendEntry appends e to res.Entries, enforcing the returned-line cap.
func appendEntry(res *Result, opts *Options, e Entry) bool {
	if len(res.Entries) >= opts.LimitLines {
		return false
	}
	res.Entries = append(res.Entries, e)
	return true
}

// sortEntriesByTime orders entries chronologically (stable). Every entry has a
// UTC RFC3339 string, whose lexicographic order matches time order.
func sortEntriesByTime(entries []Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i].Time, entries[j].Time
		if a == nil || b == nil {
			return a != nil && b == nil
		}
		return *a < *b
	})
}

// runTail returns the last LimitLines lines (that match m, when m has filters)
// of the newest file. Plain files are seeked to within MaxBytes of the end
// (efficient on GB files); gz files are streamed through a rolling window
// (cannot seek).
func runTail(ctx context.Context, opts Options, m *matcher, res *Result, now time.Time) (*Result, error) {
	newest := opts.Files[len(opts.Files)-1]
	resolved, err := opts.Policy.Resolve(newest)
	if err != nil {
		return nil, err
	}
	res.Files = append(res.Files, resolved)

	ring := entryRing{cap: opts.LimitLines}

	push := func(l logreader.Line) {
		lineStr := string(l.Raw)
		if m != nil && !m.matches(lineStr) {
			return
		}
		ring.push(Entry{File: resolved, Line: l.No, Text: lineStr})
	}

	var rc interface {
		Read([]byte) (int, error)
		Close() error
	}
	if logreader.IsGzip(resolved) {
		rc, err = logreader.OpenRead(resolved)
		if err != nil {
			return nil, err
		}
		defer mcClose(rc)
		_, nb, iterErr := logreader.Iterate(ctx, rc, opts.Limits.MaxBytes, func(l logreader.Line) error {
			push(l)
			return nil
		})
		res.ScannedBytes += nb
		res.Truncated = res.Truncated || iterErr != nil
		if iterErr != nil {
			res.Reason = tailReason(iterErr)
		}
	} else {
		f, opErr := os.Open(resolved)
		if opErr != nil {
			return nil, opErr
		}
		defer f.Close()
		fi, statErr := f.Stat()
		if statErr == nil {
			// Seek within MaxBytes of the end, honoring the byte budget.
			off := fi.Size() - opts.Limits.MaxBytes
			if off < 0 {
				off = 0
			}
			_, _ = f.Seek(off, os.SEEK_SET)
		}
		_, nb, iterErr := logreader.Iterate(ctx, f, opts.Limits.MaxBytes, func(l logreader.Line) error {
			push(l)
			return nil
		})
		res.ScannedBytes += nb
		res.Truncated = res.Truncated || iterErr != nil
		if iterErr != nil {
			res.Reason = tailReason(iterErr)
		}
	}

	res.Entries = ring.head()
	res.TotalMatched = len(res.Entries)
	return res, nil
}

func tailReason(err error) string {
	switch {
	case errors.Is(err, errResultFull):
		return "line limit reached"
	case errors.Is(err, logreader.ErrBudgetExceeded):
		return "byte budget (LOGLENS_MAX_BYTES) exceeded"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "query timeout reached"
	default:
		return err.Error()
	}
}

// entryRing is a fixed-capacity ring buffer that keeps insertion order.
type entryRing struct {
	items []Entry
	cap   int
	start int
}

func (r *entryRing) push(e Entry) {
	if r.cap <= 0 {
		return
	}
	if len(r.items) < r.cap {
		r.items = append(r.items, e)
		return
	}
	r.items[r.start] = e
	r.start = (r.start + 1) % r.cap
}

// head returns the ring contents in insertion order.
func (r *entryRing) head() []Entry {
	if len(r.items) < r.cap || r.start == 0 {
		return r.items
	}
	out := make([]Entry, 0, len(r.items))
	out = append(out, r.items[r.start:]...)
	out = append(out, r.items[:r.start]...)
	return out
}

func mcClose(rc interface{ Close() error }) { _ = rc.Close() }

// pattern is one compiled include/exclude term.
type pattern struct {
	re   *regexp.Regexp // non-nil => regex term
	lit  string         // literal substring (lower-cased when ignoreCase)
	ic   bool
	asRe bool
}

func (p pattern) match(line, lineLower string) bool {
	if p.re != nil {
		return p.re.MatchString(line)
	}
	if p.ic {
		return strings.Contains(lineLower, p.lit)
	}
	return strings.Contains(line, p.lit)
}

// matcher applies the include (AND) / exclude (OR) keyword filters.
type matcher struct {
	include []pattern
	exclude []pattern
	ic      bool
}

func newMatcher(opts *Options) (*matcher, error) {
	build := func(kws []string) ([]pattern, error) {
		var out []pattern
		for _, kw := range kws {
			if kw == "" {
				continue
			}
			p := pattern{ic: opts.IgnoreCase}
			if opts.UseRegex {
				pat := kw
				if opts.IgnoreCase && !strings.HasPrefix(pat, "(?i)") {
					pat = "(?i)" + pat
				}
				re, err := regexp.Compile(pat)
				if err != nil {
					return nil, fmt.Errorf("invalid regex %q: %w", kw, err)
				}
				p.re = re
				p.asRe = true
			} else if opts.IgnoreCase {
				p.lit = strings.ToLower(kw)
			} else {
				p.lit = kw
			}
			out = append(out, p)
		}
		return out, nil
	}
	m := &matcher{ic: opts.IgnoreCase}
	var err error
	if m.include, err = build(opts.Keywords); err != nil {
		return nil, err
	}
	if m.exclude, err = build(opts.Exclude); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *matcher) needsLower() bool {
	for _, p := range m.include {
		if !p.asRe && p.ic {
			return true
		}
	}
	for _, p := range m.exclude {
		if !p.asRe && p.ic {
			return true
		}
	}
	return false
}

func (m *matcher) matches(line string) bool {
	lineLower := ""
	if m.needsLower() {
		lineLower = strings.ToLower(line)
	}
	for _, p := range m.include {
		if !p.match(line, lineLower) {
			return false
		}
	}
	for _, p := range m.exclude {
		if p.match(line, lineLower) {
			return false
		}
	}
	return true
}
