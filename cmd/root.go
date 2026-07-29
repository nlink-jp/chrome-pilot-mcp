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

	"github.com/nlink-jp/chrome-pilot-mcp/internal/mcpserver"
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
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, Version)
		return 0
	}
	return serve(stdin, stdout, stderr)
}

func serve(stdin io.Reader, stdout, stderr io.Writer) int {
	// MCP owns stdout; all logging goes to stderr.
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	tr := transport.NewStdioTransport(stdin, stdout)
	srv := mcpserver.New("chrome-pilot-mcp", Version, tr, logger)
	// Tool registration lands with the CDP client layer (development
	// Phase 1); the scaffold serves an empty tool set.
	if err := srv.Serve(context.Background()); err != nil {
		logger.Error("serve", "err", err)
		return 1
	}
	return 0
}
