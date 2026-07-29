# AGENTS.md — chrome-pilot-mcp

## Project summary

Zero-dependency Chrome automation MCP server in Go. Reimplements the core
automation surface (27 tools) of ChromeDevTools/chrome-devtools-mcp by
speaking CDP (Chrome DevTools Protocol) directly over WebSocket. Raison
d'être: eliminate npm supply-chain risk — single static binary, `go.mod`
with no `require`, nothing downloaded at runtime.

**Current stage: scaffold.** The MCP server starts, answers initialize /
tools/list (empty) / tools/call, and `--version`. No browser tools yet.
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
cmd/root.go                 # stdlib flag CLI: serve (default), version/--version
internal/transport/         # stdio JSON-RPC transport (1MB lines, mutex writes)
internal/jsonrpc/           # JSON-RPC 2.0 types + standard codes
internal/mcpserver/         # MCP 2024-11-05 routing, RegisterTool, RawResult
internal/toolerr/           # structured {code,message,details} tool errors
scripts/                    # codesign/notarize/brew (shared org scripts)
docs/{en,ja}/               # RFP; en has no suffix, ja uses *.ja.md
```

Planned (not yet created): `internal/ws/` (in-house RFC 6455 client),
`internal/cdp/` (CDP client: call/event dispatch, target/session management),
`internal/browser/` (Chrome launcher/attach), `internal/tools/` (the 27 tool
handlers).

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
