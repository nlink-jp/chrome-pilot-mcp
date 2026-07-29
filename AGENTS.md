# AGENTS.md — chrome-pilot-mcp

## Project summary

Zero-dependency Chrome automation MCP server in Go. Reimplements the core
automation surface (27 tools) of ChromeDevTools/chrome-devtools-mcp by
speaking CDP (Chrome DevTools Protocol) directly over WebSocket. Raison
d'être: eliminate npm supply-chain risk — single static binary, `go.mod`
with no `require`, nothing downloaded at runtime.

**Current stage: feature-complete (27/27 tools), pre-release.** All tools
verified E2E against real headless Chrome (13-check scenario: click, fill,
select-all+type, dialog, console, network, dark-mode emulation, GIF
screencast, screenshot). Remaining before v0.1.0: release packaging.
Design background: `docs/en/chrome-pilot-mcp-rfp.md` (ja: `docs/ja/`).

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
                            #   pages/debug/input/console/network/emulation/
                            #   screencast; collectors.go = passive event state
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
- Event collectors (collectors.go) run on the CDP read loop: they must
  never issue CDP calls or take m.mu — collector state has its own mutex.
  The screencast frame ack is the one CDP call triggered by an event; it
  goes through a goroutine for that reason.
- Control/Meta key combos must carry CDP `commands` (e.g. selectAll):
  synthetic key events bypass browser shortcut handling. See editCommands.
