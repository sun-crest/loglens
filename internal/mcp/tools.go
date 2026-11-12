package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sun-crest/loglens/internal/logreader"
	"github.com/sun-crest/loglens/internal/parser"
	"github.com/sun-crest/loglens/internal/query"
	"github.com/sun-crest/loglens/internal/security"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ToolHandler is the SDK tool handler type.
type ToolHandler = sdk.ToolHandler

// decodeArgs unmarshals the raw arguments JSON into a map. Missing or empty
// arguments yield an empty map.
func decodeArgs(req *sdk.CallToolRequest) (map[string]any, error) {
	raw := req.Params.Arguments
	args := make(map[string]any)
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, fmt.Errorf("invalid tool arguments: %w", err)
		}
	}
	return args, nil
}

// errResult wraps err into a CallToolResult with IsError set, so the client
// (Claude Code) can see the failure and self-correct.
func errResult(err error) *sdk.CallToolResult {
	res := &sdk.CallToolResult{}
	res.SetError(err)
	return res
}

// textResult marshals v to pretty JSON and returns it as text content.
func textResult(v any) *sdk.CallToolResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errResult(fmt.Errorf("marshaling result: %w", err))
	}
	return &sdk.CallToolResult{
		Content: []sdk.Content{&sdk.TextContent{Text: string(b)}},
	}
}

// ---------------------------------------------------------------------------
// list_log_files
// ---------------------------------------------------------------------------

const listLogFilesDesc = `List log files inside the whitelisted directories.
Returns path (absolute), size, last-modified time and whether the file is gzip
compressed. Set "path" to a directory to scope the listing (must be inside a
LOGLENS_WHITELIST_DIRS entry); omit it to list every whitelisted directory.
"glob" matches file names (e.g. "*.log", "*.gz", "access*"); use a "/" in the
pattern to match relative paths recursively. Symlinks are not followed.`

func checkNonNegative(vals ...int) error {
	for _, v := range vals {
		if v < 0 {
			return fmt.Errorf("context/limit parameters must not be negative, got %d", v)
		}
	}
	return nil
}

func handleListLogFiles(cfg *security.Config) ToolHandler {
	return func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		args, derr := decodeArgs(req)
		if derr != nil {
			return errResult(derr), nil
		}
		path, _ := argString(args, "path")
		glob, _ := argString(args, "glob")
		recursive, _ := argBool(args, "recursive")

		var roots []string
		if path != "" {
			root, err := cfg.Resolve(path)
			if err != nil {
				return errResult(err), nil
			}
			st, err := os.Stat(root)
			if err != nil {
				return errResult(fmt.Errorf("path %q: %w", path, err)), nil
			}
			if !st.IsDir() {
				return errResult(fmt.Errorf("path %q is not a directory", path)), nil
			}
			roots = []string{root}
		} else {
			roots = cfg.ExistingDirs()
			if len(roots) == 0 {
				return errResult(errors.New("no LOGLENS_WHITELIST_DIRS configured (or none exist)")), nil
			}
		}

		var files []logreader.FileInfo
		for _, root := range roots {
			list, err := logreader.ListFiles(root, logreader.ListOptions{
				Glob:      glob,
				Recursive: recursive,
				MaxFiles:  cfg.Limits.MaxLines * 2,
			})
			if err != nil {
				return errResult(fmt.Errorf("list %q: %w", root, err)), nil
			}
			files = append(files, list...)
		}
		if files == nil {
			files = []logreader.FileInfo{}
		}
		return textResult(struct {
			Count int                  `json:"count"`
			Files []logreader.FileInfo `json:"files"`
		}{len(files), files}), nil
	}
}

// ---------------------------------------------------------------------------
// get_log_slice / search_logs shared logic
// ---------------------------------------------------------------------------

// timeBounds decodes optional start_time/end_time into a (possibly unbounded)
// window. Relative values like "-1h" and "now" are resolved against wallTime.
// When only one edge is given the window is treated as open on the other side
// (start-only bounds up at "now").
//
//nolint:unparam // wallTime also reserved for future TUI integration
func timeBounds(args map[string]any, wallTime time.Time) (start time.Time, hasStart bool, end time.Time, hasEnd bool, err error) {
	if s, ok := argString(args, "start_time"); ok && strings.TrimSpace(s) != "" {
		var ok bool
		start, ok = parser.ParseFlexible(strings.TrimSpace(s), wallTime)
		if !ok {
			return time.Time{}, false, time.Time{}, false,
				fmt.Errorf("cannot parse start_time %q (try RFC3339 like 2026-11-10T15:04:05Z, or -2h)", s)
		}
		hasStart = true
	}
	if s, ok := argString(args, "end_time"); ok && strings.TrimSpace(s) != "" {
		var ok bool
		end, ok = parser.ParseFlexible(strings.TrimSpace(s), wallTime)
		if !ok {
			return time.Time{}, false, time.Time{}, false,
				fmt.Errorf("cannot parse end_time %q (try RFC3339 like 2026-11-10T15:04:05Z, or -2h)", s)
		}
		hasEnd = true
	}
	if hasStart && hasEnd && end.Before(start) {
		return time.Time{}, false, time.Time{}, false, fmt.Errorf("end_time is before start_time")
	}
	return start, hasStart, end, hasEnd, nil
}

func buildQueryOptions(cfg *security.Config, files []string, args map[string]any, wallTime time.Time) (query.Options, error) {
	opts := query.Options{
		Files:      files,
		Policy:     cfg,
		Limits:     cfg.Limits,
		SortByTime: true,
	}

	start, hasStart, end, hasEnd, err := timeBounds(args, wallTime)
	if err != nil {
		return opts, err
	}
	opts.HasBounds = hasStart || hasEnd
	if hasStart {
		opts.Start = start
	}
	if hasEnd {
		opts.End = end
	} else if hasStart {
		opts.End = wallTime // "since <start>": slice up to now
	}

	opts.Keywords, _ = argStringList(args, "keywords")
	opts.Exclude, _ = argStringList(args, "exclude_keywords")
	opts.IgnoreCase, _ = argBool(args, "ignore_case")
	opts.UseRegex, _ = argBool(args, "use_regex")
	opts.PerFileBudget, _ = argBool(args, "per_file_budget")

	if n, ok := argInt(args, "context_before"); ok {
		opts.ContextBefore = n
	}
	if n, ok := argInt(args, "context_after"); ok {
		opts.ContextAfter = n
	}
	if n, ok := argInt(args, "limit_lines"); ok {
		opts.LimitLines = n
	}
	if err := checkNonNegative(opts.ContextBefore, opts.ContextAfter, opts.LimitLines); err != nil {
		return opts, err
	}
	return opts, nil
}

// ---------------------------------------------------------------------------
// get_log_slice
// ---------------------------------------------------------------------------

const getLogSliceDesc = `Slice a log file (or its rotation group) by start_time
and end_time, optionally filtered by keywords. The core LogLens operation:
handles ordinary .log files and .gz rotated logs transparently (numbered
rotation groups base, base.1, base.2... are merged oldest->newest).
Time bounds accept RFC3339 ("2026-11-10T15:04:05Z"), space-separated
("2026-11-10 15:04:05"), bare dates, or relative windows ("-2h", "now").
TIMESTAMP SEMANTICS: timestamps without an explicit zone are interpreted in the
server's LOCAL timezone, so copy the format the log files themselves use
(RFC3339 with "Z" for UTC logs, "YYYY-MM-DD HH:MM:SS" for local-time logs), and
use the same flavor for start_time and end_time.
If time bounds are omitted this returns the most recent lines (tail); with
keywords it returns the most recent lines matching them (great for "last call
to /api/foo"). With time bounds, the query seeks to the window start instead of
reading the whole file, so windows in the middle/end of very large logs are
reachable within the byte budget.
"keywords" (array or single string) must ALL appear in a line; every
exclude_keywords match removes the line. context_before/after add grep-style
context lines. Output entries are ordered chronologically and limited by
LOGLENS_MAX_LINES. The "per_file" array reports each scanned file's coverage
(bytes read/total, seek offset, first/last timestamp, completion) so a
truncated result shows where scanning stopped; when a plain file is seeked
(seek_offset > 0), entry "line" is the line's offset from that seek point, not
an absolute file line. For a time bounded query, lines with no parseable
timestamp are skipped and counted in "unparsed_lines".`

func handleGetLogSlice(cfg *security.Config, now func() time.Time) ToolHandler {
	return func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		args, derr := decodeArgs(req)
		if derr != nil {
			return errResult(derr), nil
		}
		path, ok := argString(args, "file_path")
		if !ok || path == "" {
			return errResult(errors.New("file_path is required")), nil
		}
		resolved, err := cfg.Resolve(path)
		if err != nil {
			return errResult(err), nil
		}
		st, err := os.Stat(resolved)
		if err != nil {
			return errResult(fmt.Errorf("file %q: %w", path, err)), nil
		}
		if st.IsDir() {
			return errResult(fmt.Errorf("file_path %q is a directory; list a directory with list_log_files", path)), nil
		}

		files := logreader.RotationSet(resolved)
		opts, err := buildQueryOptions(cfg, files, args, now())
		if err != nil {
			return errResult(err), nil
		}
		res, err := query.Run(ctx, opts)
		if err != nil {
			return errResult(err), nil
		}
		return textResult(res), nil
	}
}

// ---------------------------------------------------------------------------
// search_logs
// ---------------------------------------------------------------------------

const searchLogsDesc = `Search keywords (or regexes) across one or more log
files. "files" is a list of absolute paths (fetch candidates with
list_log_files); each path is whitelist-checked. keywords must ALL appear in a
line (AND); regex mode via use_regex:true. Optional start_time/end_time window;
exclude_keywords removes matching lines; ignore_case and context_before/after
behave like get_log_slice. Set "per_file_budget": true to give each file its
own LOGLENS_MAX_BYTES budget instead of sharing one across files (so one
oversized file does not starve the others). Timestamps use the same semantics
as get_log_slice (no-zone = server local time). Results are ordered
chronologically when a time window is given. Without time bounds the (possibly
multiple) files are scanned whole; give a start_time so the query seeks toward
the window and stays within the byte budget on large files. The response's
"per_file" array reports each scanned file's coverage (bytes read/total,
scan start offset, first/last timestamp, completion) so a truncated result
shows where scanning stopped. Bounded by LOGLENS_MAX_LINES,
LOGLENS_MAX_BYTES and LOGLENS_TIMEOUT_MS; when a bound trips, "truncated" is
set with the reason.`

func handleSearchLogs(cfg *security.Config, now func() time.Time) ToolHandler {
	return func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		args, derr := decodeArgs(req)
		if derr != nil {
			return errResult(derr), nil
		}
		files, ok := argStringList(args, "files")
		if !ok || len(files) == 0 {
			return errResult(errors.New("files is required (non-empty list of absolute paths)")), nil
		}
		opts, err := buildQueryOptions(cfg, files, args, now())
		if err != nil {
			return errResult(err), nil
		}
		// search_logs without time bounds scans whole files (old contract);
		// get_log_slice without bounds is a cheap tail of the newest file.
		opts.FullScan = true
		res, err := query.Run(ctx, opts)
		if err != nil {
			return errResult(err), nil
		}
		return textResult(res), nil
	}
}
