// Package tui hosts the interactive terminal UI. Placeholder this iteration:
// the MCP capability is the MVP focus (per project plan), Bubbletea comes in a
// later milestone.
package tui

import (
	"context"
	"errors"

	"github.com/sun-crest/loglens/internal/security"
)

// Run starts the interactive TUI. Not implemented yet.
func Run(ctx context.Context, cfg *security.Config) error {
	_ = ctx
	_ = cfg
	return errors.New("the TUI is not implemented yet (available in a later LogLens milestone); for now use `loglens mcp` with Claude Code, or read logs directly")
}
