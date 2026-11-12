package query

import (
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sun-crest/loglens/internal/logreader"
	"github.com/sun-crest/loglens/internal/security"
)

const testTS = "2026-11-10T12:00:%02d.000Z"

// makeLog writes n timestamped lines 12:00:00..12:00:n-1 into file.
func makeLog(t *testing.T, file string, n int) {
	t.Helper()
	var sb strings.Builder
	for i := 0; i < n; i++ {
		level := "INFO"
		if i%3 == 1 {
			level = "ERROR"
		}
		sb.WriteString(fmt.Sprintf(testTS, i) + " " + level + " msg-" + fmt.Sprintf("%02d", i) + " payload=" + fmt.Sprintf("%02d", i) + "\n")
	}
	if err := os.WriteFile(file, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gzipFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := gzip.NewWriter(f)
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func newCfg(t *testing.T) (*security.Config, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := &security.Config{
		Dirs: []string{dir},
		Limits: security.Limits{
			MaxLines: 5000,
			MaxBytes: 10 << 20,
			Timeout:  5 * time.Second,
		},
	}
	return cfg, dir
}

func mustRun(t *testing.T, opts Options) *Result {
	t.Helper()
	res, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func textOf(res *Result) []string {
	var out []string
	for _, e := range res.Entries {
		out = append(out, e.Text)
	}
	return out
}

func TestTimeSlice(t *testing.T) {
	cfg, dir := newCfg(t)
	file := filepath.Join(dir, "app.log")
	makeLog(t, file, 60)

	res := mustRun(t, Options{
		Policy: cfg, Limits: cfg.Limits, Files: []string{file},
		HasBounds: true,
		Start:     time.Date(2026, 11, 10, 12, 0, 10, 0, time.UTC),
		End:       time.Date(2026, 11, 10, 12, 0, 19, 0, time.UTC),
	})
	if res.TotalMatched != 10 {
		t.Fatalf("matched = %d, want 10 (seconds 10..19)", res.TotalMatched)
	}
	first := res.Entries[0].Text
	if !strings.Contains(first, "msg-10") {
		t.Errorf("first entry should be msg-10, got %q", first)
	}
	last := res.Entries[len(res.Entries)-1].Text
	if !strings.Contains(last, "msg-19") {
		t.Errorf("last entry should be msg-19, got %q", last)
	}
	// Chronological order within the window.
	for i := 1; i < len(res.Entries); i++ {
		if res.Entries[i-1].Text > res.Entries[i].Text {
			t.Errorf("entries out of order at %d", i)
		}
	}
}

func TestKeywordsAndExclude(t *testing.T) {
	cfg, dir := newCfg(t)
	file := filepath.Join(dir, "k.log")
	lines := []string{
		"INFO request id=42 user=alice action=login",
		"INFO request id=42 user=bob action=logout",
		"ERROR request id=42 user=alice action=login",
		"INFO request id=7 user=eve action=login",
	}
	if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// AND semantics: all keywords must appear.
	res := mustRun(t, Options{Policy: cfg, Limits: cfg.Limits, Files: []string{file},
		Keywords: []string{"id=42", "alice"}})
	if res.TotalMatched != 2 {
		t.Errorf("AND keywords matched %d, want 2", res.TotalMatched)
	}

	// Exclude (OR), case-sensitive: line3's uppercase "ERROR" and line2's
	// "logout" are dropped; only line1 survives.
	res = mustRun(t, Options{Policy: cfg, Limits: cfg.Limits, Files: []string{file},
		Keywords: []string{"id=42"}, Exclude: []string{"ERROR", "logout"}})
	if res.TotalMatched != 1 {
		t.Errorf("exclude matched %d, want 1", res.TotalMatched)
	}

	// IgnoreCase.
	res = mustRun(t, Options{Policy: cfg, Limits: cfg.Limits, Files: []string{file},
		Keywords: []string{"error"}, IgnoreCase: true})
	if res.TotalMatched != 1 {
		t.Errorf("ignore case matched %d, want 1", res.TotalMatched)
	}

	// Regex AND single keyword.
	res = mustRun(t, Options{Policy: cfg, Limits: cfg.Limits, Files: []string{file},
		Keywords: []string{`id=\d+`}, UseRegex: true})
	if res.TotalMatched != 4 {
		t.Errorf("regex matched %d, want 4", res.TotalMatched)
	}
}

func TestContextLines(t *testing.T) {
	cfg, dir := newCfg(t)
	file := filepath.Join(dir, "c.log")
	lines := []string{"line1", "line2", "line3", "line4", "line5", "killer", "line7", "line8"}
	if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := mustRun(t, Options{Policy: cfg, Limits: cfg.Limits, Files: []string{file},
		Keywords: []string{"line4"}, ContextBefore: 2, ContextAfter: 1})
	if len(res.Entries) != 1+2+1 {
		t.Fatalf("context entries = %d, want 4", len(res.Entries))
	}
	if res.Entries[0].Text != "line2" || res.Entries[2].Text != "line4" || res.Entries[3].Text != "line5" {
		t.Errorf("context mismatch: %v", textOf(res))
	}
}

func TestGzAndRotationMerge(t *testing.T) {
	cfg, dir := newCfg(t)
	base := filepath.Join(dir, "api")
	// api (newest) + api.1 + api.2.gz
	if err := os.WriteFile(base, []byte(
		"2026-11-10T12:00:02.000Z INFO newer\n"+
			"2026-11-10T12:00:03.000Z INFO newest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base+".1", []byte(
		"2026-11-10T12:00:01.000Z INFO middle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gzipFile(t, mustWriteTemp(t, "2026-11-10T12:00:00.000Z INFO oldest\n"), base+".2.gz")

	res := mustRun(t, Options{Policy: cfg, Limits: cfg.Limits, Files: logreader.RotationSet(base),
		HasBounds: true,
		Start:     time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC),
		End:       time.Date(2026, 11, 10, 12, 0, 4, 0, time.UTC)})
	if res.TotalMatched != 4 {
		t.Fatalf("rotation merge matched %d, want 4", res.TotalMatched)
	}
	// Oldest -> newest order.
	wantFirst, wantLast := "oldest", "newest"
	if !strings.Contains(res.Entries[0].Text, wantFirst) {
		t.Errorf("first entry should be %q, got %q", wantFirst, res.Entries[0].Text)
	}
	if !strings.Contains(res.Entries[len(res.Entries)-1].Text, wantLast) {
		t.Errorf("last entry should be %q, got %q", wantLast, res.Entries[len(res.Entries)-1].Text)
	}
	// Files attribute should list rotation members oldest->newest.
	if got := len(res.Files); got != 3 {
		t.Errorf("scanned files = %d, want 3 (%v)", got, res.Files)
	}
}

func mustWriteTemp(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp("", "logline-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(content)
	f.Close()
	return f.Name()
}

func TestTailMode(t *testing.T) {
	cfg, dir := newCfg(t)
	file := filepath.Join(dir, "tail.log")
	makeLog(t, file, 40)
	res := mustRun(t, Options{
		Policy: cfg, Limits: cfg.Limits, Files: []string{file},
		LimitLines: 5,
	})
	if res.TotalMatched != 5 {
		t.Fatalf("tail returned %d, want last 5", res.TotalMatched)
	}
	if !strings.Contains(res.Entries[0].Text, "msg-35") {
		t.Errorf("tail should start at msg-35, got %q", res.Entries[0].Text)
	}
	if !strings.Contains(res.Entries[len(res.Entries)-1].Text, "msg-39") {
		t.Errorf("tail should end at msg-39, got %q", res.Entries[len(res.Entries)-1].Text)
	}
}

func TestTailGz(t *testing.T) {
	cfg, dir := newCfg(t)
	base := filepath.Join(dir, "gzlog")
	if err := os.WriteFile(base+".1", []byte(
		"2026-11-10T12:00:00.000Z INFO old\n"+
			"2026-11-10T12:00:01.000Z INFO newer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gzipFile(t, base+".1", base+".1.gz")
	gz := base + ".1.gz" // single gz set

	res := mustRun(t, Options{Policy: cfg, Limits: cfg.Limits, Files: []string{gz}, LimitLines: 1})
	if res.TotalMatched != 1 {
		t.Fatalf("gz tail returned %d, want 1", res.TotalMatched)
	}
	if !strings.Contains(res.Entries[0].Text, "newer") {
		t.Errorf("gz tail should return last line, got %q", res.Entries[0].Text)
	}
}

func TestLineLimitTruncation(t *testing.T) {
	cfg, dir := newCfg(t)
	file := filepath.Join(dir, "many.log")
	makeLog(t, file, 100)
	res := mustRun(t, Options{Policy: cfg, Limits: cfg.Limits, Files: []string{file},
		HasBounds:  true,
		Start:      time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC),
		End:        time.Date(2026, 11, 10, 12, 0, 59, 0, time.UTC),
		LimitLines: 10})
	// Scanning aborts at the cap: entries are capped, matched counts only what
	// was examined before the stop.
	if !res.Truncated {
		t.Error("expected truncated=true when line limit hit")
	}
	if len(res.Entries) != 10 {
		t.Errorf("entries = %d, want 10 (capped)", len(res.Entries))
	}
	if res.TotalMatched < 10 {
		t.Errorf("TotalMatched = %d, want at least 10", res.TotalMatched)
	}
}

func TestByteBudgetTruncation(t *testing.T) {
	cfg, dir := newCfg(t)
	file := filepath.Join(dir, "big.log")
	if err := os.WriteFile(file, []byte(strings.Repeat("x", 2000)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lim := cfg.Limits
	lim.MaxBytes = 100
	cfg2 := &security.Config{Dirs: cfg.Dirs, Limits: lim}
	res := mustRun(t, Options{Policy: cfg2, Limits: lim, Files: []string{file},
		HasBounds: true,
		Start:     time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		End:       time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)})
	if !res.Truncated {
		t.Error("expected truncated=true for byte budget")
	}
	if !strings.Contains(res.Reason, "byte budget") {
		t.Errorf("reason = %q, want byte budget mention", res.Reason)
	}
}

func TestPathOutsideWhitelistRejected(t *testing.T) {
	cfg, dir := newCfg(t)
	outside := filepath.Join(filepath.Dir(dir), "outside.log")
	if err := os.WriteFile(outside, []byte("nope\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Run(context.Background(), Options{Policy: cfg, Limits: cfg.Limits, Files: []string{outside}})
	if err == nil {
		t.Fatal("expected error for path outside whitelist")
	}
}

func TestUnparsedLinesCounted(t *testing.T) {
	cfg, dir := newCfg(t)
	file := filepath.Join(dir, "mixed.log")
	content := "2026-11-10T12:00:00.000Z INFO ok\n" +
		"raw line without timestamp\n" +
		"2026-11-10T12:00:05.000Z INFO ok2\n"
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	res := mustRun(t, Options{Policy: cfg, Limits: cfg.Limits, Files: []string{file},
		HasBounds: true,
		Start:     time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC),
		End:       time.Date(2026, 11, 10, 12, 0, 9, 0, time.UTC)})
	if res.UnparsedLines != 1 {
		t.Errorf("UnparsedLines = %d, want 1", res.UnparsedLines)
	}
	if res.TotalMatched != 2 {
		t.Errorf("matched = %d, want 2", res.TotalMatched)
	}
}

// bigLog writes n lines, each about lineLen bytes with an increasing
// timestamp, so the file is many times larger than the byte budget.
func bigLog(t *testing.T, file string, n, lineLen int) {
	t.Helper()
	var sb strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, "2026-11-10T12:%02d:%02d.%06dZ ", i/60, i%60, i%1000000)
		sb.WriteString(strings.Repeat("x", lineLen-40))
		sb.WriteString(" marker\n")
	}
	if err := os.WriteFile(file, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSeekLocatesStart verifies that a time-bounded query on a file much
// larger than the byte budget can still reach a window in the middle/later
// part (P0: no more blind spot at the tail of oversized files).
func TestSeekLocatesStart(t *testing.T) {
	cfg, dir := newCfg(t)
	lim := cfg.Limits
	lim.MaxBytes = 1 << 20 // 1 MiB budget
	cfg2 := &security.Config{Dirs: cfg.Dirs, Limits: lim}

	file := filepath.Join(dir, "big.log")
	// ~3000 lines * ~1200 bytes ≈ 3.6 MiB, budget only covers the first ~1 MiB.
	bigLog(t, file, 3000, 1200)

	// Window at ~minute 30 (line ~1800, ~2.2 MiB in): beyond the 1 MiB budget
	// from the head, reachable only via seek.
	start := time.Date(2026, 11, 10, 12, 30, 10, 0, time.UTC)
	end := time.Date(2026, 11, 10, 12, 30, 20, 0, time.UTC)
	res := mustRun(t, Options{Policy: cfg2, Limits: lim, Files: []string{file},
		HasBounds: true, Start: start, End: end})

	if res.Truncated {
		t.Fatalf("truncated=true (%s): window should be within budget after seek", res.Reason)
	}
	if res.TotalMatched == 0 {
		t.Fatalf("no matches: seek did not reach the window (scanned=%d)", res.ScannedBytes)
	}
	for i, e := range res.Entries {
		if e.Time == nil {
			t.Fatalf("entry %d missing time", i)
		}
	}
	// Every returned line must be within [start, end].
	for _, e := range res.Entries {
		tt, err := time.Parse(time.RFC3339Nano, *e.Time)
		if err != nil {
			t.Fatalf("bad entry time %q: %v", *e.Time, err)
		}
		if tt.Before(start) || tt.After(end) {
			t.Errorf("entry outside window: %v", tt)
		}
	}
}

// TestSeekHandlesOutOfRangeStart: a start far before the file must not break
// the query (seek falls back to the head).
func TestSeekHandlesEarlyStart(t *testing.T) {
	cfg, dir := newCfg(t)
	file := filepath.Join(dir, "early.log")
	makeLog(t, file, 40)
	res := mustRun(t, Options{Policy: cfg, Limits: cfg.Limits, Files: []string{file},
		HasBounds: true,
		Start:     time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		End:       time.Date(2026, 11, 10, 12, 0, 39, 0, time.UTC)})
	if res.TotalMatched != 40 {
		t.Errorf("matched = %d, want 40 (all lines)", res.TotalMatched)
	}
}

// TestPerFileScanMeta asserts that results carry per-file coverage: after a
// seeked time-slice the FileScan records BytesTotal, a nonzero SeekOffset,
// scanned timestamps and completion.
func TestPerFileScanMeta(t *testing.T) {
	cfg, dir := newCfg(t)
	file := filepath.Join(dir, "meta.log")
	bigLog(t, file, 300, 1200) // ~360 KB, comfortably within the 10 MiB budget

	start := time.Date(2026, 11, 10, 12, 2, 0, 0, time.UTC)
	end := time.Date(2026, 11, 10, 12, 4, 0, 0, time.UTC)
	res := mustRun(t, Options{Policy: cfg, Limits: cfg.Limits, Files: []string{file},
		HasBounds: true, Start: start, End: end})

	if len(res.PerFile) != 1 {
		t.Fatalf("per_file = %d entries, want 1", len(res.PerFile))
	}
	ps := res.PerFile[0]
	if ps.File != file {
		t.Errorf("per_file.file = %q", ps.File)
	}
	if ps.BytesTotal <= 0 {
		t.Errorf("BytesTotal = %d, want > 0", ps.BytesTotal)
	}
	if ps.BytesRead <= 0 {
		t.Errorf("BytesRead = %d, want > 0", ps.BytesRead)
	}
	if !ps.Complete {
		t.Error("Complete = false, want true (whole file within budget)")
	}
	if ps.SeekOffset <= 0 {
		t.Errorf("SeekOffset = %d, want > 0 (window is past the file head)", ps.SeekOffset)
	}
	if ps.ScannedFrom == nil {
		t.Error("ScannedFrom nil")
	} else if *ps.ScannedFrom > "2026-11-10T12:04:00Z" {
		t.Errorf("ScannedFrom = %s, want before window end", *ps.ScannedFrom)
	}
}

// TestPerFileBudget verifies per_file_budget: with several files, each gets
// its own MaxBytes instead of the first (largest) file consuming everything.
// Uses bounds so both files go through the full-file path (tail would only
// look at the newest file).
func TestPerFileBudget(t *testing.T) {
	cfg, dir := newCfg(t)
	lim := cfg.Limits
	lim.MaxBytes = 2 << 10 // tiny: 2 KiB per file
	cfg2 := &security.Config{Dirs: cfg.Dirs, Limits: lim}

	// fileA is just under the budget (1.5 KiB); fileB is also small. With a
	// shared budget, scanning A would leave almost nothing for B; with
	// per-file budget both are fully scanned.
	writeLine := func(path string, n int) {
		var sb strings.Builder
		for i := 0; i < n; i++ {
			sb.WriteString(strings.Repeat("y", 50) + "\n")
		}
		if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a := filepath.Join(dir, "a.log")
	b := filepath.Join(dir, "b.log")
	writeLine(a, 25) // 25*51 ≈ 1275 B
	writeLine(b, 25)

	res := mustRun(t, Options{Policy: cfg2, Limits: lim, Files: []string{a, b},
		Keywords: []string{"yyyy"}, PerFileBudget: true,
		HasBounds: true,
		Start:     time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		End:       time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)})
	if len(res.PerFile) != 2 {
		t.Fatalf("per_file = %d, want 2", len(res.PerFile))
	}
	for i, ps := range res.PerFile {
		if !ps.Complete {
			t.Errorf("file %d (%s) not complete: read=%d/%d", i, ps.File, ps.BytesRead, ps.BytesTotal)
		}
	}
	if res.Truncated {
		t.Errorf("truncated=%v (should not be: each file within its own budget)", res.Truncated)
	}
}

// TestPerFileBudgetTruncatedOneStillScansOthers: with per_file_budget, a file
// that exceeds the per-file budget is truncated, but the other files are still
// scanned (no cascade abort as with the shared budget).
func TestPerFileBudgetTruncatedOneStillScansOthers(t *testing.T) {
	cfg, dir := newCfg(t)
	lim := cfg.Limits
	lim.MaxBytes = 2 << 10
	cfg2 := &security.Config{Dirs: cfg.Dirs, Limits: lim}

	var sbA strings.Builder
	for i := 0; i < 60; i++ { // 60 * 1001 B ≈ 60 KiB > 2 KiB budget
		sbA.WriteString(strings.Repeat("z", 1000) + "\n")
	}
	a := filepath.Join(dir, "a.log")
	b := filepath.Join(dir, "b.log")
	if err := os.WriteFile(a, []byte(sbA.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	var sbB strings.Builder
	for i := 0; i < 25; i++ {
		sbB.WriteString(strings.Repeat("y", 50) + "\n")
	}
	if err := os.WriteFile(b, []byte(sbB.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	res := mustRun(t, Options{Policy: cfg2, Limits: lim, Files: []string{a, b},
		Keywords: []string{"yyyy"}, PerFileBudget: true,
		HasBounds: true,
		Start:     time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		End:       time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)})
	if !res.Truncated {
		t.Error("expected truncated=true (a.log exceeds its per-file budget)")
	}
	if len(res.PerFile) != 2 {
		t.Fatalf("per_file = %d, want 2 (b.log must still be scanned)", len(res.PerFile))
	}
	seenB := false
	for _, ps := range res.PerFile {
		if strings.HasSuffix(ps.File, "a.log") && ps.Complete {
			t.Errorf("a.log should be incomplete (over budget)")
		}
		if strings.HasSuffix(ps.File, "b.log") {
			seenB = true
			if !ps.Complete {
				t.Errorf("b.log should be complete under per-file budget, read=%d/%d", ps.BytesRead, ps.BytesTotal)
			}
		}
	}
	if !seenB {
		t.Error("b.log was never scanned (per-file budget must not cascade-abort)")
	}
}

// TestSharedBudgetFirstFileStarves asserts the old behavior is preserved
// without per_file_budget: scanning a big first file exhausts the shared
// budget and the second file never gets scanned.
func TestSharedBudgetFirstFileStarves(t *testing.T) {
	cfg, dir := newCfg(t)
	lim := cfg.Limits
	lim.MaxBytes = 2 << 10
	cfg2 := &security.Config{Dirs: cfg.Dirs, Limits: lim}

	var sbA strings.Builder
	for i := 0; i < 100; i++ {
		sbA.WriteString(strings.Repeat("z", 1000) + "\n")
	}
	a := filepath.Join(dir, "a.log")
	b := filepath.Join(dir, "b.log")
	if err := os.WriteFile(a, []byte(sbA.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	var sbB strings.Builder
	for i := 0; i < 25; i++ {
		sbB.WriteString(strings.Repeat("y", 50) + "\n")
	}
	if err := os.WriteFile(b, []byte(sbB.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	res := mustRun(t, Options{Policy: cfg2, Limits: lim, Files: []string{a, b},
		Keywords: []string{"yyyy"},
		HasBounds: true,
		Start:     time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		End:       time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)})
	if !res.Truncated {
		t.Error("expected truncated=true with a shared tiny budget")
	}
	for _, ps := range res.PerFile {
		if strings.HasSuffix(ps.File, "a.log") && ps.Complete {
			t.Errorf("a.log should be incomplete (budget spent partway), read=%d/%d", ps.BytesRead, ps.BytesTotal)
		}
		if strings.HasSuffix(ps.File, "b.log") {
			t.Errorf("b.log should not be scanned at all under a shared exhausted budget, got read=%d", ps.BytesRead)
		}
	}
}

// TestTailWithKeywords verifies that get_log_slice-style tail (no bounds, no
// context) with a keyword returns the most recent matching lines, not a
// whole-file scan (P0: reverse-ish search for the latest occurrence).
func TestTailWithKeywords(t *testing.T) {
	cfg, dir := newCfg(t)
	file := filepath.Join(dir, "tw.log")
	var sb strings.Builder
	for i := 0; i < 30; i++ {
		line := fmt.Sprintf("2026-11-10T12:00:%02d.000Z INFO msg-%02d", i, i)
		if i%3 == 0 {
			line += " WANT"
		}
		sb.WriteString(line + "\n")
	}
	if err := os.WriteFile(file, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	res := mustRun(t, Options{Policy: cfg, Limits: cfg.Limits, Files: []string{file},
		LimitLines: 5, Keywords: []string{"WANT"}})
	if res.Truncated {
		t.Fatalf("truncated: %s", res.Reason)
	}
	if res.TotalMatched != 5 {
		t.Errorf("matched = %d, want exactly 5 (limit)", res.TotalMatched)
	}
	// Most recent matching lines first (msg-27 is the last WANT at i=27).
	last := res.Entries[len(res.Entries)-1].Text
	if !strings.Contains(last, "msg-27") {
		t.Errorf("last entry should be the newest match msg-27, got %q", last)
	}
}
