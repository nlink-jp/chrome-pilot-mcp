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
	"slices"
	"strconv"
	"strings"

	"github.com/nlink-jp/chrome-pilot-mcp/internal/config"
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

	// --version is handled inside buildConfig's flag set, so parse first.
	if slices.Contains(args, "--version") || slices.Contains(args, "-version") {
		fmt.Fprintln(stdout, Version)
		return 0
	}

	cfg, err := buildConfig(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return serve(cfg, stdin, stdout, stderr)
}

// buildConfig parses flags, loads config.toml, and merges them into the
// runtime configuration. Flags win, but only when explicitly given.
func buildConfig(args []string) (tools.Config, error) {
	fs := flag.NewFlagSet("chrome-pilot-mcp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	_ = fs.Bool("version", false, "print version and exit")
	configPath := fs.String("config", "", "path to config.toml (default: ~/.config/chrome-pilot-mcp/config.toml if present)")
	headless := fs.Bool("headless", false, "launch Chrome headless")
	channel := fs.String("channel", "stable", "Chrome channel to launch: stable|beta|dev|canary")
	execPath := fs.String("executable-path", "", "explicit Chrome binary (overrides --channel)")
	attach := fs.String("attach", "", "attach to an existing Chrome debugging endpoint (ws://..., port, or host:port; loopback only) instead of launching")
	workspaceRoot := fs.String("workspace-root", "", "output directory for screenshots etc. (default: a fresh temp dir)")
	viewport := fs.String("viewport", "", "initial viewport as WxH, e.g. 1280x800")
	profile := fs.String("profile", "", "named persistent browser profile (kept across runs; letters, digits, _ and - only)")
	userDataDir := fs.String("user-data-dir", "", "explicit Chrome user-data-dir (persistent; exclusive with --profile)")
	allowHosts := fs.String("allow-hosts", "", "comma-separated host allow list; specifying any switches to default-deny (e.g. \"example.com,*.example.com\")")
	blockHosts := fs.String("block-hosts", "", "comma-separated host block list; takes precedence over --allow-hosts")
	blockLocal := fs.Bool("block-local", false, "refuse navigation to file:// and data: URLs")
	if err := fs.Parse(args); err != nil {
		return tools.Config{}, err
	}

	fileCfg, err := loadConfig(*configPath)
	if err != nil {
		return tools.Config{}, err
	}

	// Flags win over the file, but only when actually given: an unset flag
	// carries its zero value and must not clobber a configured setting.
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["headless"] {
		fileCfg.Headless = *headless
	}
	if set["channel"] {
		fileCfg.Channel = *channel
	}
	if set["executable-path"] {
		fileCfg.ExecutablePath = *execPath
	}
	if set["attach"] {
		fileCfg.Attach = *attach
	}
	if set["workspace-root"] {
		fileCfg.WorkspaceRoot = *workspaceRoot
	}
	if set["viewport"] {
		fileCfg.Viewport = *viewport
	}
	if set["profile"] {
		fileCfg.Profile = *profile
	}
	if set["user-data-dir"] {
		fileCfg.UserDataDir = *userDataDir
	}
	if set["allow-hosts"] {
		fileCfg.AllowHosts = splitList(*allowHosts)
	}
	if set["block-hosts"] {
		fileCfg.BlockHosts = splitList(*blockHosts)
	}
	if set["block-local"] {
		fileCfg.BlockLocal = *blockLocal
	}

	return toToolConfig(fileCfg)
}

// loadConfig reads an explicit --config path (missing = error) or the
// single default location (missing = defaults). The working directory is
// never searched — see ADR-0002.
func loadConfig(explicit string) (config.Config, error) {
	if explicit != "" {
		return config.Load(explicit)
	}
	path, err := config.DefaultPath()
	if err != nil {
		// No user config dir on this platform: run on defaults.
		return config.Default(), nil
	}
	cfg, _, err := config.LoadIfExists(path)
	return cfg, err
}

// splitList parses a comma-separated flag value.
func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return []string{}
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// toToolConfig validates the merged configuration and converts it to the
// runtime shape.
func toToolConfig(c config.Config) (tools.Config, error) {
	out := tools.Config{
		Headless:       c.Headless,
		Channel:        c.Channel,
		ExecutablePath: c.ExecutablePath,
		Attach:         c.Attach,
		WorkspaceRoot:  c.WorkspaceRoot,
		Profile:        c.Profile,
		UserDataDir:    c.UserDataDir,
		AllowHosts:     c.AllowHosts,
		BlockHosts:     c.BlockHosts,
		BlockLocal:     c.BlockLocal,
	}
	if c.Viewport != "" {
		w, h, err := parseViewport(c.Viewport)
		if err != nil {
			return out, err
		}
		out.ViewportWidth, out.ViewportHeight = w, h
	}
	// Profile options are launch-only: an attached browser already has its
	// profile (ADR-0003). Catch the contradiction at startup rather than on
	// the first tool call.
	if c.Attach != "" && (c.Profile != "" || c.UserDataDir != "") {
		return out, fmt.Errorf("--attach cannot be combined with --profile or --user-data-dir " +
			"(an already-running Chrome keeps its own profile)")
	}
	if c.Profile != "" && c.UserDataDir != "" {
		return out, fmt.Errorf("--profile and --user-data-dir are mutually exclusive")
	}
	return out, nil
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

	m := tools.NewManager(cfg, logger)
	defer m.Shutdown()
	tools.RegisterAll(srv, m)

	if err := srv.Serve(context.Background()); err != nil {
		logger.Error("serve", "err", err)
		return 1
	}
	return 0
}
