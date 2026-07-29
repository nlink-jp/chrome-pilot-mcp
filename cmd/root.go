// Package cmd implements the CLI entry point.
//
// chrome-pilot-mcp is zero-dependency by design (no external Go modules —
// see CLAUDE.md), so the org's usual cobra scaffold is deliberately replaced
// with stdlib flag parsing.
package cmd

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/nlink-jp/chrome-pilot-mcp/internal/mcpserver"
	"github.com/nlink-jp/chrome-pilot-mcp/internal/tools"
	"github.com/nlink-jp/chrome-pilot-mcp/internal/transport"
)

// Version is overridden at build time via -ldflags "-X .../cmd.Version=<vX.Y.Z>".
var Version = "dev"

// Execute parses os.Args and runs the CLI, exiting the process with the
// resulting status code.
func Execute() {
	os.Exit(Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// Run is the testable entry point behind Execute.
//
// With no arguments it serves MCP over stdin/stdout. `version` and
// `--version` print the version; both spellings must print the identical
// string because the shared homebrew formula template runs `--version` in
// its test block.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "version" {
		fmt.Fprintln(stdout, Version)
		return 0
	}
	// `serve` is the implicit default; accept it explicitly too so client
	// configs can spell out the intent.
	if len(args) > 0 && args[0] == "serve" {
		args = args[1:]
	}

	fs := flag.NewFlagSet("chrome-pilot-mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	showVersion := fs.Bool("version", false, "print version and exit")
	headless := fs.Bool("headless", false, "launch Chrome headless")
	channel := fs.String("channel", "stable", "Chrome channel to launch: stable|beta|dev|canary")
	execPath := fs.String("executable-path", "", "explicit Chrome binary (overrides --channel)")
	attach := fs.String("attach", "", "attach to an existing Chrome debugging endpoint (ws://..., port, or host:port; loopback only) instead of launching")
	workspaceRoot := fs.String("workspace-root", "", "output directory for screenshots etc. (default: a fresh temp dir)")
	viewport := fs.String("viewport", "", "initial viewport as WxH, e.g. 1280x800")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, Version)
		return 0
	}

	cfg := tools.Config{
		Headless:       *headless,
		Channel:        *channel,
		ExecutablePath: *execPath,
		Attach:         *attach,
		WorkspaceRoot:  *workspaceRoot,
	}
	if *viewport != "" {
		w, h, err := parseViewport(*viewport)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		cfg.ViewportWidth, cfg.ViewportHeight = w, h
	}
	return serve(cfg, stdin, stdout, stderr)
}

// parseViewport parses "WxH".
func parseViewport(s string) (int, int, error) {
	ws, hs, ok := strings.Cut(s, "x")
	if !ok {
		return 0, 0, fmt.Errorf("--viewport must be WxH, e.g. 1280x800 (got %q)", s)
	}
	w, err1 := strconv.Atoi(ws)
	h, err2 := strconv.Atoi(hs)
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 0, 0, fmt.Errorf("--viewport must be WxH with positive integers (got %q)", s)
	}
	return w, h, nil
}

func serve(cfg tools.Config, stdin io.Reader, stdout, stderr io.Writer) int {
	// MCP owns stdout; all logging goes to stderr.
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	tr := transport.NewStdioTransport(stdin, stdout)
	srv := mcpserver.New("chrome-pilot-mcp", Version, tr, logger)

	m := tools.NewManager(cfg)
	defer m.Shutdown()
	tools.RegisterAll(srv, m)

	if err := srv.Serve(context.Background()); err != nil {
		logger.Error("serve", "err", err)
		return 1
	}
	return 0
}
