# AGENTS.md — chrome-pilot-mcp

## Project summary

Zero-dependency Chrome automation MCP server in Go. Reimplements the core
automation surface (27 tools) of ChromeDevTools/chrome-devtools-mcp by
speaking CDP (Chrome DevTools Protocol) directly over WebSocket. Raison
d'être: eliminate npm supply-chain risk — single static binary, `go.mod`
with no `require`, nothing downloaded at runtime.

**Current stage: released (v0.2.0), 27/27 tools.** All tools verified E2E
against real headless Chrome, plus config.toml, profile persistence, and
host-filter enforcement. v0.2.0 came entirely out of using v0.1.0 as an
MCP client — see the CHANGELOG for what that turned up.

Design background: `docs/en/chrome-pilot-mcp-rfp.md` (ja: `docs/ja/`) and
the ADRs — 0001 host allow/block lists, 0002 config.toml, 0003 browser
profiles (all Accepted).

## Build & test

```bash
make build    # → dist/chrome-pilot-mcp (never `go build` directly)
make test     # go test ./...
make package  # release archives (zip/tar.gz) + notarization
make brew     # after package: render the formula into the local homebrew-tap
```

## Structure

```
main.go                     # calls cmd.Execute()
cmd/root.go                 # stdlib flag CLI: serve (default), version/--version, flags
internal/transport/         # stdio JSON-RPC transport (1MB lines, mutex writes)
internal/jsonrpc/           # JSON-RPC 2.0 types + standard codes
internal/mcpserver/         # MCP 2024-11-05 routing, RegisterTool, RawResult
internal/toolerr/           # structured {code,message,details} tool errors
internal/config/            # config.toml: TOML subset parser + schema
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
- Never kill launched Chrome without giving it time to exit: it writes
  profile data during shutdown, and killing early silently drops
  localStorage/cookies from persistent profiles (browser.closeGrace).
- Config precedence lives in `cmd.buildConfig`: only flags reported by
  `flag.Visit` override the file, so an unset flag's zero value can never
  clobber a configured setting. Never search `./config.toml` (ADR-0002).
- Host filtering must stay startup-only: no tool may widen it at run
  time, or a prompt-injected agent could unlock itself.
- Any CDP call that can trigger a JS dialog must go through
  `callGuarded` (dialogguard.go): alert/confirm/prompt block the renderer,
  so an unguarded call waits out its whole timeout on a successful action.
- The accessibility tree is not the whole page. Unnamed clickable elements
  only reach the agent through the DOM pass in extranodes.go; keep the
  uid map able to hold a nodeId as well as a backendNodeId.
