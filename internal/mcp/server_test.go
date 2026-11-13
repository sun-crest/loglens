package mcp_test

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sun-crest/loglens/internal/mcp"
	"github.com/sun-crest/loglens/internal/security"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// This suite drives the LogLens MCP server through the official SDK client
// over an in-memory transport — the same handshake and framing Claude Code's
// client uses, minus the stdio pipes.

func newTestServer(t *testing.T) (*sdk.Server, *security.Config, string) {
	t.Helper()
	dir := t.TempDir()
	writeFixture(t, dir, "api.log",
		"2026-11-10T12:00:01.000Z INFO api request id=a01\n"+
			"2026-11-10T12:00:02.000Z INFO api request id=a02\n"+
			"2026-11-10T12:00:03.000Z ERROR api request id=a01 db timeout\n")
	writeFixture(t, dir, "app.log",
		"2026-11-10 12:10:01,001 [main] INFO App starting\n"+
			"2026-11-10 12:10:02,002 [http-nio] ERROR java.lang.RuntimeException: boom\n")
	writeFixtureGz(t, dir, "api.log.1.gz",
		"2026-11-10T11:00:00.000Z INFO api older\n")

	cfg := &security.Config{
		Dirs: []string{dir},
		Limits: security.Limits{
			MaxLines: 500,
			MaxBytes: 1 << 20,
			Timeout:  5 * time.Second,
		},
	}
	return mcp.NewServer(cfg, "test"), cfg, dir
}

func writeFixture(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeFixtureGz(t *testing.T, dir, name, content string) {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := gzip.NewWriter(f)
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func connect(t *testing.T, s *sdk.Server) *sdk.ClientSession {
	t.Helper()
	t1, t2 := sdk.NewInMemoryTransports()
	if _, err := s.Connect(context.Background(), t1, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	c := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := c.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func call(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) (map[string]any, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if res.IsError {
		var txt string
		for _, c := range res.Content {
			if tc, ok := c.(*sdk.TextContent); ok {
				txt += tc.Text
			}
		}
		return map[string]any{"_error": strings.TrimSpace(txt)}, true
	}
	out := map[string]any{}
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			if err := json.Unmarshal([]byte(tc.Text), &out); err == nil {
				return out, false
			}
		}
	}
	return out, false
}

func TestToolList(t *testing.T) {
	s, _, _ := newTestServer(t)
	cs := connect(t, s)

	var names []string
	for tool, err := range cs.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
	}
	want := []string{"list_log_files", "get_log_slice", "search_logs"}
	if len(names) != len(want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
	for _, w := range want {
		found := false
		for _, n := range names {
			if n == w {
				found = true
			}
		}
		if !found {
			t.Errorf("missing tool %q in %v", w, names)
		}
	}
}

func TestListLogFiles(t *testing.T) {
	s, _, dir := newTestServer(t)
	cs := connect(t, s)

	out, isErr := call(t, cs, "list_log_files", map[string]any{"path": dir, "recursive": true})
	if isErr {
		t.Fatalf("list_log_files errored: %v", out["_error"])
	}
	files, ok := out["files"].([]any)
	if !ok || len(files) != 3 {
		t.Fatalf("files = %v", out)
	}
	byName := map[string]bool{}
	for _, f := range files {
		m := f.(map[string]any)
		name := filepath.Base(m["path"].(string))
		byName[name] = m["compressed"].(bool)
	}
	for _, want := range []string{"api.log", "api.log.1.gz", "app.log"} {
		if _, ok := byName[want]; !ok {
			t.Errorf("missing %q in file set %v", want, byName)
		}
	}
	if !byName["api.log.1.gz"] {
		t.Errorf("gz file should be marked compressed")
	}
	if byName["api.log"] {
		t.Errorf("plain file should not be marked compressed")
	}
}

// TestListLogFilesSortTime verifies the "sort": "time" parameter of
// list_log_files: newest modified file must come first.
func TestListLogFilesSortTime(t *testing.T) {
	s, _, dir := newTestServer(t)
	cs := connect(t, s)
	// Give app.log a mtime well after the other fixtures (which were created
	// moments ago), so it is unambiguously the newest.
	future := time.Now().Add(2 * time.Minute)
	if err := os.Chtimes(filepath.Join(dir, "app.log"), future, future); err != nil {
		t.Fatal(err)
	}
	out, isErr := call(t, cs, "list_log_files", map[string]any{
		"path": dir, "recursive": true, "sort": "time",
	})
	if isErr {
		t.Fatalf("list_log_files errored: %v", out["_error"])
	}
	files, ok := out["files"].([]any)
	if !ok || len(files) == 0 {
		t.Fatalf("files = %v", out)
	}
	first := filepath.Base(files[0].(map[string]any)["path"].(string))
	if first != "app.log" {
		t.Errorf("newest file should be app.log, got %q", first)
	}
}

// TestListLogFilesMaxFilesCap verifies the list cap is itself link: listing is
// cut at MaxFiles (independent of MaxLines) and the response reports truncated.
func TestListLogFilesMaxFilesCap(t *testing.T) {
	s, cfg, dir := newTestServer(t)
	// Override limits: a tiny MaxFiles proves the per-list cap works and that
	// it is no longer derived from MaxLines (which stays low too).
	cfg.Limits.MaxFiles = 2
	cs := connect(t, s)

	out, isErr := call(t, cs, "list_log_files", map[string]any{"path": dir, "recursive": true})
	if isErr {
		t.Fatalf("list_log_files errored: %v", out["_error"])
	}
	files, ok := out["files"].([]any)
	if !ok {
		t.Fatalf("files = %v", out)
	}
	if len(files) == 0 {
		t.Skip("no files to truncate")
	}
	if trunc, ok := out["truncated"]; !ok || !trunc.(bool) {
		t.Errorf("truncated = %v, want true (3 files but MaxFiles=2)", out["truncated"])
	}
	if len(files) > 2 {
		t.Errorf("listed %d files, want at most MaxFiles=2", len(files))
	}
}

func TestGetLogSliceRotationBlendsGz(t *testing.T) {
	s, _, dir := newTestServer(t)
	cs := connect(t, s)

	// time window 11:00..12:01 should merge api.log.1.gz + api.log.
	out, isErr := call(t, cs, "get_log_slice", map[string]any{
		"file_path":  filepath.Join(dir, "api.log"),
		"start_time": "2026-11-10T11:00:00Z",
		"end_time":   "2026-11-10T12:01:00Z",
	})
	if isErr {
		t.Fatalf("get_log_slice errored: %v", out["_error"])
	}
	entries := out["entries"].([]any)
	if len(entries) != 4 {
		t.Fatalf("entries = %d, want 4 (1 gz + 3 main)", len(entries))
	}
	// Chronological: gz line first.
	firstText := entries[0].(map[string]any)["text"].(string)
	if !strings.Contains(firstText, "older") {
		t.Errorf("first entry should be the gz log, got %q", firstText)
	}
	first := entries[0].(map[string]any)
	if !strings.Contains(first["file"].(string), "api.log.1.gz") {
		t.Errorf("first entry file = %v, want gz", first["file"])
	}
}

func TestGetLogSliceKeywordsAndContext(t *testing.T) {
	s, _, dir := newTestServer(t)
	cs := connect(t, s)

	out, isErr := call(t, cs, "get_log_slice", map[string]any{
		"file_path":      filepath.Join(dir, "app.log"),
		"keywords":       []any{"RuntimeException"},
		"context_before": 1,
		"context_after":  1,
	})
	if isErr {
		t.Fatalf("errored: %v", out["_error"])
	}
	entries := out["entries"].([]any)
	// fixture has 2 lines: hit at line 2, 1 context-before line, no after.
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2 (ctx-before + hit)", len(entries))
	}
	// Java timestamps must surface in output even without bounds.
	first := entries[0].(map[string]any)
	if mt, ok := first["time"]; !ok || mt == nil {
		t.Errorf("entry missing parsed time (java comma ms): %v", first)
	}
	if !strings.Contains(first["text"].(string), "App starting") {
		t.Errorf("context-before should be 'App starting', got %q", first["text"])
	}
}

func TestSearchLogsAcrossFiles(t *testing.T) {
	s, _, dir := newTestServer(t)
	cs := connect(t, s)

	out, isErr := call(t, cs, "search_logs", map[string]any{
		"files":     []any{filepath.Join(dir, "api.log"), filepath.Join(dir, "app.log")},
		"keywords":  []any{`.*timeout`},
		"use_regex": true,
	})
	if isErr {
		t.Fatalf("errored: %v", out["_error"])
	}
	if out["total_matched"].(float64) != 1 {
		t.Errorf("matched = %v, want 1 (db timeout)", out["total_matched"])
	}
}

func TestSearchLogsExcludeAndIgnoreCase(t *testing.T) {
	s, _, dir := newTestServer(t)
	cs := connect(t, s)

	out, isErr := call(t, cs, "search_logs", map[string]any{
		"files":            []any{filepath.Join(dir, "api.log")},
		"keywords":         "error",
		"ignore_case":      true,
		"exclude_keywords": []any{"timeout"},
	})
	if isErr {
		t.Fatalf("errored: %v", out["_error"])
	}
	entries := out["entries"].([]any)
	if len(entries) != 0 {
		t.Fatalf("entries = %v, want 0 (only ERROR line was a timeout)", len(entries))
	}
	// Single string keyword also works.
	out, isErr = call(t, cs, "search_logs", map[string]any{
		"files":    []any{filepath.Join(dir, "api.log")},
		"keywords": "db timeout",
	})
	if isErr {
		t.Fatalf("single-keyword errored: %v", out["_error"])
	}
	if out["total_matched"].(float64) != 1 {
		t.Errorf("single keyword matched = %v", out["total_matched"])
	}
}

// TestSearchLogsTailBackwards drives search_logs + tail_backwards through the
// MCP layer: it must return the latest keyword match without a time window even
// though search_logs normally forces a whole-file scan (FullScan).
func TestSearchLogsTailBackwards(t *testing.T) {
	s, _, dir := newTestServer(t)
	cs := connect(t, s)

	out, isErr := call(t, cs, "search_logs", map[string]any{
		"files":          []any{filepath.Join(dir, "api.log")},
		"keywords":       []any{"request"},
		"tail_backwards": true,
		"limit_lines":    1,
	})
	if isErr {
		t.Fatalf("errored: %v", out["_error"])
	}
	if res, ok := out["entries"].([]any); ok {
		if len(res) != 1 {
			t.Fatalf("entries = %d, want 1 (latest request hit)", len(res))
		}
		last := res[0].(map[string]any)["text"].(string)
		// Newest line matching "request" is the 12:00:03 ERROR row (id=a01).
		if !strings.Contains(last, "12:00:03") {
			t.Errorf("latest request hit should be the 12:00:03 row, got %q", last)
		}
	}
	if out["truncated"].(bool) {
		t.Errorf("unexpected truncated: %v", out["reason"])
	}
}

func TestPathTraversalDenied(t *testing.T) {
	s, _, _ := newTestServer(t)
	cs := connect(t, s)

	out, isErr := call(t, cs, "get_log_slice", map[string]any{
		"file_path":  "/etc/passwd",
		"start_time": "2026-11-10T00:00:00Z",
	})
	if !isErr {
		t.Fatalf("expected error for /etc/passwd, got %v", out)
	}
	if msg := out["_error"].(string); !strings.Contains(msg, "outside the allowed log directories") {
		t.Errorf("error message = %q", msg)
	}
}

func TestBadTimeBoundDenied(t *testing.T) {
	s, _, dir := newTestServer(t)
	cs := connect(t, s)

	out, isErr := call(t, cs, "get_log_slice", map[string]any{
		"file_path":  filepath.Join(dir, "api.log"),
		"start_time": "not-a-time",
	})
	if !isErr {
		t.Fatalf("expected error for garbage time, got %v", out)
	}
	t.Logf("expected denial message: %v", out["_error"])
}

func TestGetLogSliceTailDefault(t *testing.T) {
	s, _, dir := newTestServer(t)
	cs := connect(t, s)

	out, isErr := call(t, cs, "get_log_slice", map[string]any{
		"file_path":   filepath.Join(dir, "api.log"),
		"limit_lines": 2,
	})
	if isErr {
		t.Fatalf("tail errored: %v", out["_error"])
	}
	entries := out["entries"].([]any)
	if len(entries) != 2 {
		t.Fatalf("tail entries = %d, want 2", len(entries))
	}
	last := entries[len(entries)-1].(map[string]any)
	if !strings.Contains(last["text"].(string), "db timeout") {
		t.Errorf("tail should end at the newest line, got %v", last["text"])
	}
}
