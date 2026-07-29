# AGENTS.md — chrome-pilot-mcp

## Project summary

Zero-dependency Chrome automation MCP server in Go. Reimplements the core
automation surface (27 tools) of ChromeDevTools/chrome-devtools-mcp by
speaking CDP (Chrome DevTools Protocol) directly over WebSocket. Raison
d'être: eliminate npm supply-chain risk — single static binary, `go.mod`
with no `require`, nothing downloaded at runtime.

**Current stage: Phase 1 core implemented.** 9 tools work E2E against real
Chrome: list/new/select/close/navigate_page, wait_for, evaluate_script,
take_snapshot, take_screenshot. Remaining: input (10), console (2),
network (2), emulation (2), screencast GIF (2).
Full plan: `docs/en/chrome-pilot-mcp-rfp.md` (ja: `docs/ja/`).

## Build & test

```bash
make build    # → dist/chrome-pilot-mcp (never `go build` directly)
make test     # go test ./...
make package  # release archives (zip/tar.gz) + notarization
```

## Structure

```
main.go                     # calls cmd.Execute()
cmd/root.go                 # stdlib flag CLI: serve (default), version/--version, flags
internal/transport/         # stdio JSON-RPC transport (1MB lines, mutex writes)
internal/jsonrpc/           # JSON-RPC 2.0 types + standard codes
internal/mcpserver/         # MCP 2024-11-05 routing, RegisterTool, RawResult
internal/toolerr/           # structured {code,message,details} tool errors
internal/ws/                # in-house RFC 6455 client (ws:// loopback only)
internal/cdp/               # CDP client: id correlation, sessions, events
internal/browser/           # Chrome launch/attach, executable discovery
internal/tools/             # Manager (page/session state) + tool handlers
scripts/                    # codesign/notarize/brew (shared org scripts)
docs/{en,ja}/               # RFP; en has no suffix, ja uses *.ja.md
```

## Gotchas

- **Zero-dependency policy is load-bearing.** Never add an external Go module
  — not cobra, not a websocket library, nothing. stdlib only. This is the
  project's reason to exist; see CLAUDE.md.
- `cmd/` deliberately deviates from the org's cobra scaffold (stdlib `flag`).
  `--version` and `version` must keep printing identical strings — a test
  pins this, and the shared homebrew formula's `brew test` calls `--version`.
- The 4 skeleton packages under `internal/` are ported from data-toolbox-mcp;
  keep fixes in sync conceptually (they are copies, not a shared module —
  the org forbids local `replace` directives).
- MCP owns stdout. All logging must go to stderr (slog), never stdout.
- Tool names/schemas mirror the Apache-2.0 upstream; keep the inspired-by
  attribution in both READMEs when renaming or adding tools.
- Unit tests never touch Chrome: `internal/tools` tests run against the
  in-memory `fakeChrome` (a scripted `cdp.Conn`). Real-Chrome verification
  is a manual E2E (pipe JSON-RPC lines into the built binary with
  `--headless`); run it before any release.
- The browser connects lazily on the first tool call — initialize and
  tools/list must keep working without Chrome installed.
