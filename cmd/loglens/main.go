// Command loglens is the LogLens binary: a local log workspace with a native
// MCP server for Claude Code (loglens mcp) and a planned interactive TUI
// (loglens tui).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sun-crest/loglens/internal/mcp"
	"github.com/sun-crest/loglens/internal/security"
	"github.com/sun-crest/loglens/internal/tui"
)

const version = "0.2.0"

func main() {
	log.SetFlags(0) // plain messages; all diagnostics go to stderr (stdout is the MCP transport)

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "mcp":
			if err := runMCP(); err != nil {
				log.Fatal(err)
			}
			return
		case "serve":
			if err := runServe(); err != nil {
				log.Fatal(err)
			}
			return
		case "tui":
			if err := runTUI(); err != nil {
				log.Fatal(err)
			}
			return
		case "version", "--version", "-v":
			fmt.Println("loglens " + version)
			return
		case "help", "--help", "-h":
			usage()
			return
		default:
			fmt.Fprintf(os.Stderr, "loglens: unknown subcommand %q\n", os.Args[1])
			usage()
			os.Exit(2)
		}
	}
	// No subcommand: default to the MCP server (that is how Claude Code starts us).
	if err := runMCP(); err != nil {
		log.Fatal(err)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `LogLens `, version, ` - a local log workspace with a native MCP server for Claude Code.

Usage:
  loglens mcp       Run the MCP stdio server (for Claude Code; default command)
  loglens serve     Run the MCP Streamable HTTP server (remote access)
  loglens tui       Interactive TUI (not implemented yet)
  loglens version   Print the version
  loglens help      Show this help

Security configuration (environment variables):
  LOGLENS_WHITELIST_DIRS   Comma-separated allowed log directories (required)
  LOGLENS_MAX_LINES        Max lines returned per query            (default 2000)
  LOGLENS_MAX_BYTES        Max bytes read from disk per query      (default 10485760)
  LOGLENS_TIMEOUT_MS       Max wall-clock time per query (ms)      (default 15000)

serve flags:
  --addr   Listen address (default 127.0.0.1:8787; keep on loopback and expose via SSH tunnel)
  --token  Optional bearer token; requires Authorization: Bearer <token> on every request

Example:
  LOGLENS_WHITELIST_DIRS=/var/log,/opt/service/logs loglens mcp
  LOGLENS_WHITELIST_DIRS=/var/log,/opt/service/logs loglens serve --addr 127.0.0.1:8787 --token secret
`)
}

func loadConfigOrExit() *security.Config {
	cfg, err := security.LoadConfigFromEnv()
	if err != nil {
		log.Fatalf("loglens: invalid configuration: %v", err)
	}
	if !cfg.HasDirs() {
		log.Print("loglens: warning: LOGLENS_WHITELIST_DIRS is empty; all file access will be denied " +
			"(set it to the directories you want LogLens to manage)")
	}
	return cfg
}

func runMCP() error {
	cfg := loadConfigOrExit()
	if missing := missingWhitelistDirs(cfg); len(missing) > 0 {
		log.Printf("loglens: warning: whitelist directories do not exist (yet): %v", missing)
	}
	return mcp.Run(context.Background(), cfg, version)
}

// runServe exposes the MCP server over Streamable HTTP, so a local Claude Code
// can reach logs on a remote host. Defaults to loopback only; cross-machine
// access should go through an SSH tunnel, with --token enabled.
func runServe() error {
	cfg := loadConfigOrExit()

	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8787", "listen address (keep on loopback; expose via SSH tunnel)")
	token := fs.String("token", "", "require Authorization: Bearer <token> on every request")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("serve: unexpected argument %q", fs.Arg(0))
	}
	if missing := missingWhitelistDirs(cfg); len(missing) > 0 {
		log.Printf("loglens: warning: whitelist directories do not exist (yet): %v", missing)
	}
	if *token == "" {
		log.Print("loglens: serve: no --token set; bind stays on loopback — do not publish the port without a token")
	}

	handler := mcp.NewHTTPHandler(cfg, version, mcp.HTTPOptions{Token: *token, Path: "/mcp"})
	srv := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("loglens serve: Streamable MCP listening on http://%s/mcp", *addr)
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	case <-ctx.Done():
		log.Print("loglens serve: shutting down")
		shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shCtx)
	}
}

func runTUI() error {
	cfg := loadConfigOrExit()
	return tui.Run(context.Background(), cfg)
}

func missingWhitelistDirs(cfg *security.Config) []string {
	existing := map[string]bool{}
	for _, d := range cfg.ExistingDirs() {
		existing[d] = true
	}
	var missing []string
	for _, d := range cfg.Dirs {
		if !existing[d] {
			missing = append(missing, d)
		}
	}
	return missing
}
