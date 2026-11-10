// Package mcp exposes LogLens to Claude Code: a native MCP server over stdio,
// built on the official modelcontextprotocol/go-sdk. Every tool handler is
// constructed with the security bounds baked in.
package mcp

import (
	"context"
	"time"

	"github.com/sun-crest/loglens/internal/security"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// schema helpers build the JSON Schema objects advertised to clients.
func prop(typ string, desc string, extra ...map[string]any) map[string]any {
	p := map[string]any{"type": typ, "description": desc}
	for _, e := range extra {
		for k, v := range e {
			p[k] = v
		}
	}
	return p
}

func strProp(desc string) map[string]any  { return prop("string", desc) }
func intProp(desc string) map[string]any  { return prop("integer", desc) }
func boolProp(desc string) map[string]any { return prop("boolean", desc) }
func strArrayProp(desc string) map[string]any {
	return prop("array", desc, map[string]any{"items": map[string]any{"type": "string"}})
}
func strArrayOrStringProp(desc string) map[string]any {
	return prop("array", desc, map[string]any{
		"items": map[string]any{"type": "string"},
		"oneOf": []any{
			map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			map[string]any{"type": "string"},
		},
	})
}

func addTool(s *sdk.Server, name, desc string, props map[string]any, required []string, h ToolHandler) {
	schema := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	s.AddTool(&sdk.Tool{Name: name, Description: desc, InputSchema: schema}, h)
}

// NewServer assembles the LogLens MCP server with the three MVP tools.
func NewServer(cfg *security.Config, version string) *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{Name: "loglens", Version: version}, &sdk.ServerOptions{
		Instructions: "LogLens: local log workspace. All file access is confined to LOGLENS_WHITELIST_DIRS; queries are capped at " +
			"LOGLENS_MAX_LINES lines, LOGLENS_MAX_BYTES bytes and LOGLENS_TIMEOUT_MS. Prefer get_log_slice with explicit start/end " +
			"time bounds; enumerate candidates first with list_log_files.",
	})

	now := time.Now

	addTool(s, "list_log_files", listLogFilesDesc, map[string]any{
		"path":      strProp("Directory to list (absolute). Optional: defaults to all whitelisted directories."),
		"glob":      strProp("Pattern matched against file base name, e.g. \"*.log\", \"*.gz\", \"access*\". A pattern containing '/' is matched against paths relative to the root directory."),
		"recursive": boolProp("Recurse into subdirectories. Default false."),
	}, nil, handleListLogFiles(cfg))

	addTool(s, "get_log_slice", getLogSliceDesc, map[string]any{
		"file_path":        strProp("Absolute path of the log file (plain or .gz). Rotation siblings are merged automatically."),
		"start_time":       strProp("Slice start. RFC3339 (2026-11-10T15:04:05Z), space-separated (2026-11-10 15:04:05), bare date (2026-11-10), or relative (-2h, -30m)."),
		"end_time":         strProp("Slice end (same formats). Optional: defaults to the query time."),
		"keywords":         strArrayOrStringProp("Keywords that must ALL appear in a line (AND). Optional."),
		"exclude_keywords": strArrayOrStringProp("Lines matching ANY of these are excluded (OR). Optional."),
		"ignore_case":      boolProp("Case-insensitive matching. Default false."),
		"use_regex":        boolProp("Treat keywords as regular expressions. Default false."),
		"context_before":   intProp("Context lines to include before each hit. Default 0."),
		"context_after":    intProp("Context lines to include after each hit. Default 0."),
		"limit_lines":      intProp("Maximum lines to return (capped by LOGLENS_MAX_LINES). Optional."),
	}, []string{"file_path"}, handleGetLogSlice(cfg, now))

	addTool(s, "search_logs", searchLogsDesc, map[string]any{
		"files":            strArrayProp("Absolute paths of log files to search (whitelist-checked)."),
		"keywords":         strArrayOrStringProp("Keywords that must ALL appear in a line (AND). Optional."),
		"exclude_keywords": strArrayOrStringProp("Lines matching ANY of these are excluded (OR). Optional."),
		"use_regex":        boolProp("Treat keywords as regular expressions. Default false."),
		"ignore_case":      boolProp("Case-insensitive matching. Default false."),
		"start_time":       strProp("Optional window start (same formats as get_log_slice)."),
		"end_time":         strProp("Optional window end (same formats as get_log_slice)."),
		"context_before":   intProp("Context lines before each hit. Default 0."),
		"context_after":    intProp("Context lines after each hit. Default 0."),
		"limit_lines":      intProp("Maximum lines to return (capped by LOGLENS_MAX_LINES). Optional."),
	}, []string{"files"}, handleSearchLogs(cfg, now))

	return s
}

// Run starts the server over stdin/stdout until the client disconnects.
func Run(ctx context.Context, cfg *security.Config, version string) error {
	s := NewServer(cfg, version)
	return s.Run(ctx, &sdk.StdioTransport{})
}
