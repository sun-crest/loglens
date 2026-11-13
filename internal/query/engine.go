// Package query implements the log slicing & search engine that backs the MCP
// tools. Everything funnels resource enforcement (line limits, byte budget,
// timeouts) set by the security package.
package query

import (
	"bufio"
	"bytes"
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
	File          string  `json:"file"`
	Line          int64   `json:"line"`
	Time          *string `json:"time,omitempty"` // RFC3339Nano (UTC) when parsed
	Text          string  `json:"text"`
	TextTruncated bool    `json:"text_truncated,omitempty"` // text cut at MaxLineChars
}

// FileScan summarizes how one file was scanned, so the MCP caller can see the
// coverage of a (possibly truncated) query and interpret entry line numbers.
type FileScan struct {
	File        string  `json:"file"`
	BytesRead   int64   `json:"bytes_read"`              // decoded bytes consumed from this file
	BytesTotal  int64   `json:"bytes_total,omitempty"`   // file size; 0 when unknown (gz/cannot stat)
	SeekOffset  int64   `json:"seek_offset,omitempty"`   // byte offset scanning started at; entry Line is relative to it when > 0
	Complete    bool    `json:"complete"`                // reached EOF (not cut by budget/limit/timeout)
	ScannedFrom *string `json:"scanned_from,omitempty"`  // timestamp of the first scanned line
	ScannedTo   *string `json:"scanned_to,omitempty"`    // timestamp of the last scanned line
}

// Result is what a slice/search returns to the MCP caller.
type Result struct {
	Files         []string   `json:"files"`         // files that were actually scanned
	Entries       []Entry    `json:"entries"`       // hit + context lines, in read order
	TotalMatched  int        `json:"total_matched"` // lines matching the filters (excludes context)
	ScannedBytes  int64      `json:"scanned_bytes"`
	UnparsedLines int64      `json:"unparsed_lines,omitempty"` // lines without a parseable timestamp in a time-bounded query
	Truncated     bool       `json:"truncated"`                // true if a limit cut results short
	Reason        string     `json:"reason,omitempty"`
	PerFile       []FileScan `json:"per_file"` // per-file scan coverage (omitempty when empty)
	Summary       string     `json:"summary,omitempty"` // human-readable guidance for truncated / empty results

	// internal accounting, not serialized:
	returnedChars int  // bytes of entry text appended so far
	charCapped    bool // MaxResultChars tripped (distinguishes from line cap)
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
	// TailBackwards scans a plain file from its end toward the head and returns
	// the most recent LimitLines matches — an efficient "latest occurrence of
	// keyword X" primitive, effective even when the target sits in the middle of
	// a large file (no time-window guessing). Conflicting with time bounds /
	// context; gz files fall back to a forward scan with a rolling match window.
	TailBackwards bool
	// PerFileBudget gives each file its own MaxBytes instead of sharing one
	// global budget across files, so one oversized file cannot starve the rest.
	PerFileBudget bool
	// MaxResultChars caps the total chars of returned entry text (0 = uncapped).
	// Entries beyond the cap are dropped and results flagged truncated, matching
	// per_line_limit semantics — protects the MCP response from token blow-up.
	MaxResultChars int
	// MaxLineChars caps a single returned line's text (0 = uncapped). Longer
	// lines are cut and Entry.TextTruncated is set, so one mega-JSON line cannot
	// blow up the response on its own.
	MaxLineChars int
}

// tailMode reports whether the query reads only the tail window of the newest
// file: no time bounds, no context lines. Keywords/exclude filters are allowed
// and are applied inside the tail window (returns the most recent matching
// lines). FullScan suppresses tail so search stays whole-file. TailBackwards
// forces the reverse-tail path even when FullScan is set (search_logs).
func (o *Options) tailMode() bool {
	if o.FullScan && !o.TailBackwards {
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

	res := &Result{Entries: []Entry{}, PerFile: []FileScan{}}

	// No time bounds, no context: cheap tail of the newest file. Keywords /
	// excludes (if any) are applied inside the tail window, returning the most
	// recent matching lines.
	if opts.tailMode() {
		return runTail(ctx, opts, m, res, now)
	}

	for _, f := range opts.Files {
		if ctx.Err() != nil {
			break
		}
		if !opts.PerFileBudget && res.Truncated {
			// Shared budget already exhausted: further files have nothing left.
			break
		}
		if err := runFile(ctx, f, &opts, m, res, now); err != nil {
			return nil, err
		}
	}

	if opts.SortByTime && opts.HasBounds && opts.ContextBefore == 0 && opts.ContextAfter == 0 {
		sortEntriesByTime(res.Entries)
	}
	buildSummary(res, &opts)
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
	fs := FileScan{File: resolved}

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
			fs.BytesTotal = st.Size()
			off, probe := locateStartOffset(fh, st.Size(), opts.Start, now)
			res.ScannedBytes += probe
			fs.SeekOffset = off
			if _, serr := fh.Seek(off, io.SeekStart); serr != nil {
				return serr
			}
			return scanReader(ctx, fh, resolved, opts, m, res, &fs, now)
		}
	}

	rc, err := logreader.OpenRead(resolved)
	if err != nil {
		return err
	}
	defer rc.Close()
	res.Files = append(res.Files, resolved)
	return scanReader(ctx, rc, resolved, opts, m, res, &fs, now)
}

// scanReader streams rc through the filter pipeline sharing the runFile logic.
// fs is filled with the per-file coverage (bytes read, timestamps seen,
// completion flag) so a truncated result carries enough context to retry.
func scanReader(ctx context.Context, rc io.Reader, resolved string, opts *Options, m *matcher, res *Result, fs *FileScan, now time.Time) error {
	budget := opts.Limits.MaxBytes - res.ScannedBytes
	if opts.PerFileBudget {
		budget = opts.Limits.MaxBytes
	}
	if budget <= 0 {
		res.Truncated = true
		res.Reason = "byte budget (LOGLENS_MAX_BYTES) exceeded"
		res.PerFile = append(res.PerFile, *fs)
		return nil
	}

	var (
		before         []Entry
		afterRemaining int
	)
	_, nb, scanErr := logreader.Iterate(ctx, rc, budget, func(l logreader.Line) error {
		lineStr := string(l.Raw)

		// Parse the line timestamp once; an absent timestamp means "unparsed"
		// for time-bounded queries and "no surfaced time" otherwise.
		tt, hasTime := parser.ExtractLineTime(l.Raw, now)
		if hasTime {
			s := tt.UTC().Format(time.RFC3339Nano)
			if fs.ScannedFrom == nil {
				fs.ScannedFrom = &s
			}
			fs.ScannedTo = &s
		}

		var entryTime *string
		if opts.HasBounds {
			if !hasTime {
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
		} else if hasTime {
			// No bounds: surface the timestamp already parsed above (one parse
			// per line, no duplicate ExtractLineTime call).
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
	fs.BytesRead += nb

	switch {
	case scanErr == nil || errors.Is(scanErr, errWindowPassed):
		fs.Complete = true
	case errors.Is(scanErr, errResultFull):
		res.Truncated = true
		if res.charCapped {
			res.Reason = "result char limit (max_result_chars) exceeded"
		} else {
			res.Reason = "line limit reached"
		}
	case errors.Is(scanErr, logreader.ErrBudgetExceeded):
		res.Truncated = true
		res.Reason = "byte budget (LOGLENS_MAX_BYTES) exceeded"
	case errors.Is(scanErr, context.DeadlineExceeded), errors.Is(scanErr, context.Canceled):
		res.Truncated = true
		res.Reason = "query timeout reached"
	default:
		res.PerFile = append(res.PerFile, *fs)
		return scanErr
	}
	res.PerFile = append(res.PerFile, *fs)
	return nil
}

// pushContext appends e to a bounded context-before buffer, dropping the
// oldest line when the buffer overflows.
func pushContext(buf []Entry, e Entry, cap int) []Entry {
	if len(buf) >= cap {
		buf = buf[1:]
	}
	return append(buf, e)
}

// capText caps text at opts.MaxLineChars (0 = uncapped), cutting at a rune
// boundary so multi-byte UTF-8 is not split. The caller sets TextTruncated from
// the boolean. Single mega-JSON lines cannot blow up a response on their own.
func (o *Options) capText(text string) (string, bool) {
	if o.MaxLineChars <= 0 || len(text) <= o.MaxLineChars {
		return text, false
	}
	// Walk back to the last rune start so we never split a rune.
	cut := o.MaxLineChars
	for cut > 0 && cut < len(text) && text[cut]&0xC0 == 0x80 {
		cut--
	}
	return text[:cut], true
}

// appendEntry appends e to res.Entries, enforcing the returned-line cap and
// the returned-char budget. Entry text is capped at MaxLineChars first, so the
// char budget measures post-capping bytes.
func appendEntry(res *Result, opts *Options, e Entry) bool {
	if opts.MaxLineChars > 0 {
		if t, on := opts.capText(e.Text); on {
			e.Text = t
			e.TextTruncated = true
		}
	}
	if len(res.Entries) >= opts.LimitLines {
		return false
	}
	if opts.MaxResultChars > 0 && res.returnedChars+len(e.Text) > opts.MaxResultChars {
		res.charCapped = true
		return false
	}
	res.returnedChars += len(e.Text)
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
// (cannot seek). Entries' Line is relative to the file head only when the
// whole file was read (SeekOffset 0); otherwise it is the line's offset from
// SeekOffset, and the per-file scan carries SeekOffset so the caller can
// reconstruct the absolute position.
func runTail(ctx context.Context, opts Options, m *matcher, res *Result, now time.Time) (*Result, error) {
	newest := opts.Files[len(opts.Files)-1]
	resolved, err := opts.Policy.Resolve(newest)
	if err != nil {
		return nil, err
	}
	res.Files = append(res.Files, resolved)
	fs := FileScan{File: resolved}

	ring := entryRing{cap: opts.LimitLines}

	push := func(l logreader.Line) {
		lineStr := string(l.Raw)
		if m != nil && !m.matches(lineStr) {
			return
		}
		ring.push(Entry{File: resolved, Line: l.No, Text: lineStr})
	}

	record := func(l logreader.Line) {
		if t, ok := parser.ExtractLineTime(l.Raw, now); ok {
			s := t.UTC().Format(time.RFC3339Nano)
			if fs.ScannedFrom == nil {
				fs.ScannedFrom = &s
			}
			fs.ScannedTo = &s
		}
	}

	var rc interface {
		Read([]byte) (int, error)
		Close() error
	}
	if logreader.IsGzip(resolved) {
		// gz cannot seek backwards; a forward scan with a rolling match window
		// already keeps only the most recent matches, which satisfies both the
		// plain tail and reverse-tail ("latest N matches") contracts.
		rc, err = logreader.OpenRead(resolved)
		if err != nil {
			return nil, err
		}
		defer mcClose(rc)
		_, nb, iterErr := logreader.Iterate(ctx, rc, opts.Limits.MaxBytes, func(l logreader.Line) error {
			record(l)
			push(l)
			return nil
		})
		res.ScannedBytes += nb
		fs.BytesRead = nb
		fs.Complete = iterErr == nil
		res.Truncated = res.Truncated || iterErr != nil
		if iterErr != nil {
			res.Reason = tailReason(iterErr)
		}
	} else if opts.TailBackwards {
		// Reverse scan of a plain file: read chunks from the file end toward the
		// head, split complete lines, keep the most recent matches. Unlike the
		// seek-to-tail window, this covers the whole file within the byte budget,
		// so "latest occurrence of X" works even when the target sits in the
		// middle of a large log.
		if f, opErr := os.Open(resolved); opErr == nil {
			entries, rerr := reverseTailEntries(ctx, f, &opts, m, res, &fs, now)
			_ = f.Close()
			if rerr != nil {
				return nil, rerr
			}
			res.Entries = entries
		} else {
			return nil, opErr
		}
		res.TotalMatched = len(res.Entries)
		finalizeEntries(res, &opts)
		buildSummary(res, &opts)
		res.PerFile = append(res.PerFile, fs)
		return res, nil
	} else {
		f, opErr := os.Open(resolved)
		if opErr != nil {
			return nil, opErr
		}
		defer f.Close()
		if fi, statErr := f.Stat(); statErr == nil {
			fs.BytesTotal = fi.Size()
			// Seek within MaxBytes of the end, honoring the byte budget.
			off := fi.Size() - opts.Limits.MaxBytes
			if off < 0 {
				off = 0
			}
			fs.SeekOffset = off
			_, _ = f.Seek(off, os.SEEK_SET)
		}
		_, nb, iterErr := logreader.Iterate(ctx, f, opts.Limits.MaxBytes, func(l logreader.Line) error {
			record(l)
			push(l)
			return nil
		})
		res.ScannedBytes += nb
		fs.BytesRead = nb
		fs.Complete = iterErr == nil
		res.Truncated = res.Truncated || iterErr != nil
		if iterErr != nil {
			res.Reason = tailReason(iterErr)
		}
	}

	res.Entries = ring.head()
	res.TotalMatched = len(res.Entries)
	finalizeEntries(res, &opts)
	buildSummary(res, &opts)
	res.PerFile = append(res.PerFile, fs)
	return res, nil
}

// reverseChunkSize is how many bytes are read per backward chunk scan.
const reverseChunkSize = 256 << 10 // 256 KiB

// reverseTailEntries scans a plain file from its end toward the head,
// splitting complete lines (handling chunks that start mid-line) and returning
// the most recent LimitLines entries that match m. Scanning stops as soon as
// the match limit is reached, so "latest occurrence of X" reads only as much of
// the file tail as needed. Bytes read are charged to the shared budget through
// res/budget handling in the caller: this function stops early when the context
// is done; budget exhaustion is the caller's responsibility via the matcher-free
// split loop (it just stops emitting past the limit).
//
// The returned entries are ordered newest -> oldest (the order scanning
// produces); runTail's caller contract expects chronological order, so callers
// must reverse before returning.
func reverseTailEntries(ctx context.Context, f *os.File, opts *Options, m *matcher, res *Result, fs *FileScan, now time.Time) ([]Entry, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return nil, err
	}
	size, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, err
	}
	fs.BytesTotal = size

	limit := opts.LimitLines
	if limit <= 0 {
		limit = opts.Limits.MaxLines
	}

	var (
		out   []Entry // newest -> oldest
		carry []byte  // partial line head not yet emitted
		// Covered range of timestamps in file order (min = head side, max = tail
		// side), independent of the reverse scan direction.
		haveMin bool
		haveMax bool
		minT, maxT time.Time
	)

	emit := func(line []byte) {
		lineStr := strings.TrimSuffix(string(line), "\n")
		if lineStr == "" {
			return
		}
		var t *string
		if tt, ok := parser.ExtractLineTime(line, now); ok {
			s := tt.UTC().Format(time.RFC3339Nano)
			t = &s
			if !haveMin || tt.Before(minT) {
				haveMin = true
				minT = tt
			}
			if !haveMax || tt.After(maxT) {
				haveMax = true
				maxT = tt
			}
		}
		if m != nil && !m.matches(lineStr) {
			return
		}
		if len(out) < limit {
			out = append(out, Entry{File: fs.File, Text: lineStr, Time: t})
		}
	}

	// scanChunk processes one reversed chunk. buf holds bytes read from the
	// file; rev processes from the end toward the head. When the chunk starts
	// mid-line, that partial head is stored into *carry to prepend to the next
	// older chunk's tail.
	var (
		buf    []byte
		bufPos int
	)
	budget := opts.Limits.MaxBytes - res.ScannedBytes
	if budget <= 0 {
		budget = 0
	}
	pos := size
	for pos > 0 {
		if ctx.Err() != nil {
			fs.Complete = false
			res.Truncated = true
			res.Reason = "query timeout reached"
			break
		}
		n := int64(reverseChunkSize)
		if n > pos {
			n = pos
		}
		if n > budget {
			// Budget limits how much we may read this round. Clamp the chunk to
			// the remaining budget and read it; the next round detects a zero
			// budget and stops with truncated coverage.
			n = budget
		}
		if n <= 0 {
			// Byte budget exhausted before reaching the file head: stop and
			// report a partial reverse scan so the caller knows it must either
			// narrow the search or enlarge LOGLENS_MAX_BYTES.
			fs.Complete = false
			res.Truncated = true
			res.Reason = "byte budget (LOGLENS_MAX_BYTES) exceeded"
			break
		}
		start := pos - n
		pos = start
		budget -= n

		if cap(buf) < int(n) {
			buf = make([]byte, n)
		}
		buf = buf[:n]
		if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
			return nil, err
		}
		res.ScannedBytes += n
		fs.BytesRead += n

		// Walk buf from end to start, splitting on '\n'. The piece before the
		// first '\n' found scanning backward is a partial line head: prepend
		// carry (from a later chunk, if any) and carry the result forward.
		bufPos = int(n)
		// Walk buf from end to start, splitting on '\n'. A piece before the
		// first '\n' found scanning backward is a partial line head whose
		// remainder lives in an older chunk; carry it forward.
		for bufPos > 0 {
			idx := bytes.LastIndexByte(buf[:bufPos], '\n')
			if idx < 0 {
				// No newline in the scanned prefix: this piece is a partial
				// line head; prepend any carry from a later (newer) chunk since
				// carry bytes occur after these in the file, and this piece is
				// the earlier part of the same line.
				piece := buf[:bufPos]
				if carry != nil {
					merged := make([]byte, 0, len(piece)+len(carry))
					merged = append(merged, piece...)
					merged = append(merged, carry...)
					carry = merged
				} else {
					carry = append(carry, piece...)
				}
				bufPos = 0
				break
			}
			// buf[idx+1:bufPos] is a complete line. If a carry exists, the
			// carried bytes are the tail of this line (they occur later in the
			// file), so the full line is piece + carry.
			line := buf[idx+1 : bufPos]
			if carry != nil {
				merged := make([]byte, 0, len(carry)+len(line))
				merged = append(merged, line...)
				merged = append(merged, carry...)
				emit(merged)
				carry = nil
			} else {
				emit(line)
			}
			bufPos = idx
		}

		// Early exit: we have enough matches (they are the most recent).
		if len(out) >= limit {
			fs.Complete = true
			break
		}
	}
	if carry != nil && len(carry) > 0 && len(out) < limit {
		emit(carry)
		carry = nil
	}
	if haveMin {
		s := minT.UTC().Format(time.RFC3339Nano)
		fs.ScannedFrom = &s
	}
	if haveMax {
		s := maxT.UTC().Format(time.RFC3339Nano)
		fs.ScannedTo = &s
	}
	if pos <= 0 && !res.Truncated {
		fs.Complete = true
	}
	// Scanning ran newest -> oldest; reverse so the caller sees chronological
	// order with the most recent match last (the tail contract's expectation).
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// finalizeEntries caps tail/reverse result text and trims it to MaxResultChars,
// keeping the most recent entries (oldest dropped once over budget).
func finalizeEntries(res *Result, opts *Options) {
	if opts.MaxLineChars > 0 {
		for i := range res.Entries {
			if t, on := opts.capText(res.Entries[i].Text); on {
				res.Entries[i].Text = t
				res.Entries[i].TextTruncated = true
			}
		}
	}
	if opts.MaxResultChars <= 0 || len(res.Entries) == 0 {
		return
	}
	total := 0
	for _, e := range res.Entries {
		total += len(e.Text)
	}
	wasFull := len(res.Entries)
	for total > opts.MaxResultChars && len(res.Entries) > 1 {
		total -= len(res.Entries[0].Text)
		res.Entries = res.Entries[1:]
	}
	if total > opts.MaxResultChars {
		// Even a single capped line is over budget; keep the newest row anyway
		// so the caller at least sees something rather than an empty payload.
		res.Entries = res.Entries[len(res.Entries)-1:]
	}
	if len(res.Entries) < wasFull {
		res.Truncated = true
		res.charCapped = true
		res.Reason = "result char limit (max_result_chars) exceeded"
	}
}

// lastScannedTo returns the last per-file ScannedTo bound for summaries.
func lastScannedTo(res *Result) string {
	for i := len(res.PerFile) - 1; i >= 0; i-- {
		if s := res.PerFile[i].ScannedTo; s != nil && *s != "" {
			return *s
		}
	}
	return ""
}

// maxScannedFrom returns the earliest scanned_from across files for summaries.
func maxScannedFrom(res *Result) string {
	var best string
	for _, pf := range res.PerFile {
		if pf.ScannedFrom == nil {
			continue
		}
		if best == "" || *pf.ScannedFrom < best {
			best = *pf.ScannedFrom
		}
	}
	return best
}

// buildSummary fills res.Summary with a one-line human/agent-readable guide for
// truncated or empty results, so the caller can decide whether to narrow the
// window, raise a limit, or switch to a time-bounded query.
func buildSummary(res *Result, opts *Options) bool {
	if !res.Truncated && (len(res.PerFile) == 0 || res.TotalMatched > 0) {
		return false
	}
	to := lastScannedTo(res)
	switch {
	case strings.Contains(res.Reason, "timeout"):
		if to != "" {
			res.Summary = fmt.Sprintf("query timed out; scanning stopped at %s — narrow the time window or raise LOGLENS_TIMEOUT_MS", to)
		} else {
			res.Summary = "query timed out before scanning completed — narrow the time window or raise LOGLENS_TIMEOUT_MS"
		}
	case strings.Contains(res.Reason, "byte budget"):
		if to != "" {
			res.Summary = fmt.Sprintf("LOGLENS byte budget used up; scanning stopped at %s — narrow the time window or raise LOGLENS_MAX_BYTES", to)
		} else {
			res.Summary = "byte budget used up before scanning completed — narrow the time window or raise LOGLENS_MAX_BYTES"
		}
	case strings.Contains(res.Reason, "max_result_chars"):
		res.Summary = fmt.Sprintf("returned %d entries and hit max_result_chars — raise it or narrow the search (limit_lines)", len(res.Entries))
	case strings.Contains(res.Reason, "line limit"):
		res.Summary = fmt.Sprintf("returned %d entries and hit the line cap — raise limit_lines or narrow the search", len(res.Entries))
	case !res.Truncated && res.TotalMatched == 0:
		from := maxScannedFrom(res)
		if from != "" && to != "" {
			res.Summary = fmt.Sprintf("no matches in the scanned range (%s to %s); try other keywords, a wider window, or tail_backwards", from, to)
		} else {
			res.Summary = "no matches were found in the scanned range; try other keywords or a wider window"
		}
	default:
		return false
	}
	return res.Summary != ""
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
