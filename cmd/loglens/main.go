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

	"github.com/sun-crest/loglens/internal/config"
	"github.com/sun-crest/loglens/internal/logfile"
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
  loglens mcp [--root <dir>]      Run the MCP stdio server (for Claude Code; default command)
  loglens serve [--root <dir>]    Run the MCP Streamable HTTP server (remote access)
             [--addr <host:port>] [--token <token>]
  loglens tui [--root <dir>]      Interactive TUI (not implemented yet)
  loglens version                 Print the version
  loglens help                    Show this help

Workspace (directory-tree) deployment is preferred:
  <root>/bin/loglens         run every command via this path; the root is
  <root>/config/loglens.toml   detected from the binary location, and the config
  <root>/logs/               file supplies dirs/limits/serve/log defaults.
Move the whole <root> directory to another host - nothing to reconfigure.
Without a workspace, configuration falls back to environment variables:

Security configuration (environment variables):
  LOGLENS_WHITELIST_DIRS   Comma-separated allowed log directories
  LOGLENS_MAX_LINES        Max lines returned per query            (default 2000)
  LOGLENS_MAX_BYTES        Max bytes read from disk per query      (default 10485760)
  LOGLENS_TIMEOUT_MS       Max wall-clock time per query (ms)      (default 15000)

serve flags:
  --root   Workspace root (also LOGLENS_ROOT, or inferred from bin/ + config/)
  --addr   Listen address (default from config/loglens.toml, else 127.0.0.1:8787;
           keep on loopback and expose via SSH tunnel)
  --token  Optional bearer token; overrides the config file

Examples:
  <root>/bin/loglens serve --addr 127.0.0.1:8787   # config from <root>/config/loglens.toml
  LOGLENS_WHITELIST_DIRS=/var/log,/opt/service/logs loglens mcp
`)
}

// loadSetOrExit resolves the full configuration (workspace root, config file,
// environment variables) and exits with a clear message when invalid.
func loadSetOrExit(rootFlag string) *config.Set {
	set, err := config.Load(config.Options{Root: rootFlag})
	if err != nil {
		log.Fatalf("loglens: invalid configuration: %v", err)
	}
	if !set.Config.HasDirs() {
		log.Print("loglens: warning: no log directories configured; all file access will be denied " +
			"(set config/loglens.toml dirs.allow or LOGLENS_WHITELIST_DIRS)")
	}
	return set
}

// applyRuntimeLog redirects the runtime log to a file when a workspace
// log.file is configured. It returns a closer for the file, or nil when the
// process keeps logging to stderr.
func applyRuntimeLog(set *config.Set) *logfile.Writer {
	if set.LogFile == "" {
		return nil
	}
	w, err := logfile.New(set.LogFile, set.LogMaxBytes)
	if err != nil {
		log.Printf("loglens: warning: cannot open runtime log %s: %v (falling back to stderr)", set.LogFile, err)
		return nil
	}
	log.SetOutput(w)
	return w
}

func runMCP() error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	root := fs.String("root", "", "workspace root (also read from LOGLENS_ROOT or inferred from the binary location)")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("mcp: unexpected argument %q", fs.Arg(0))
	}

	set := loadSetOrExit(*root)
	warnMissingDirs(set.Config)
	return mcp.Run(context.Background(), set.Config, version)
}

// runServe exposes the MCP server over Streamable HTTP, so a local Claude Code
// can reach logs on a remote host. Defaults to loopback only; cross-machine
// access should go through an SSH tunnel, with a token configured.
func runServe() error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	root := fs.String("root", "", "workspace root (directory-tree layout)")
	addr := fs.String("addr", "", "listen address (default from config/loglens.toml, else 127.0.0.1:8787)")
	token := fs.String("token", "", "require Authorization: Bearer <token> on every request")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("serve: unexpected argument %q", fs.Arg(0))
	}

	set := loadSetOrExit(*root)
	if rw := applyRuntimeLog(set); rw != nil {
		defer rw.Close()
	}
	warnMissingDirs(set.Config)

	// Resolved defaults come from config/env; only *explicitly passed* flags
	// override them (a default-valued flag must not clobber the config file).
	addrVal, tokenVal := set.ServeAddr, set.ServeToken
	setFlags := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { setFlags[f.Name] = true })
	if setFlags["addr"] {
		addrVal = *addr
	}
	if setFlags["token"] {
		tokenVal = *token
	}

	if tokenVal == "" {
		log.Print("loglens: serve: no token configured; bind stays on loopback - do not publish the port without a token")
	}

	handler := mcp.NewHTTPHandler(set.Config, version, mcp.HTTPOptions{Token: tokenVal, Path: "/mcp"})
	srv := &http.Server{
		Addr:              addrVal,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("loglens serve: Streamable MCP listening on http://%s/mcp", addrVal)
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
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	root := fs.String("root", "", "workspace root (directory-tree layout)")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("tui: unexpected argument %q", fs.Arg(0))
	}

	set := loadSetOrExit(*root)
	return tui.Run(context.Background(), set.Config)
}

func warnMissingDirs(cfg *security.Config) {
	if missing := missingWhitelistDirs(cfg); len(missing) > 0 {
		log.Printf("loglens: warning: whitelist directories do not exist (yet): %v", missing)
	}
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