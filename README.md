# chrome-pilot-mcp

> **Status: in development (scaffold).** The MCP server starts and speaks the
> protocol, but no browser tools are implemented yet. See the
> [RFP](docs/en/chrome-pilot-mcp-rfp.md) for the full plan.

[日本語](README.ja.md)

A zero-dependency Chrome automation MCP server. It reimplements the core
automation surface of Google's
[chrome-devtools-mcp](https://github.com/ChromeDevTools/chrome-devtools-mcp)
as a single Go binary that speaks the Chrome DevTools Protocol (CDP)
directly — no npm, no npx, no puppeteer, and no external Go modules either.

## Why

Supply-chain risk. The upstream server runs via npx with a large transitive
npm dependency tree and downloads browser binaries at runtime. chrome-pilot-mcp
is built for environments where that is unacceptable:

- **Single static binary** — nothing is fetched at install or run time
- **Zero Go module dependencies** — `go.mod` has no `require`; the WebSocket
  client (RFC 6455, localhost/plaintext only) is implemented in-house
- **Drives your installed Chrome** — launches it with a dedicated profile
  bound to `127.0.0.1`, or attaches to an existing debugging endpoint

## Planned tool surface (27 tools)

| Category | Tools |
|---|---|
| Pages (6) | `list_pages` / `new_page` / `select_page` / `close_page` / `navigate_page` / `wait_for` |
| Input (10) | `click` / `click_at` / `drag` / `fill` / `fill_form` / `hover` / `press_key` / `type_text` / `upload_file` / `handle_dialog` |
| Debugging (5) | `take_snapshot` / `take_screenshot` / `evaluate_script` / `list_console_messages` / `get_console_message` |
| Network (2) | `list_network_requests` / `get_network_request` |
| Emulation (2) | `emulate` / `resize_page` |
| Screencast (2) | `screencast_start` / `screencast_stop` (animated GIF) |

Tool names and argument schemas follow the upstream so agents can reuse
existing usage patterns. Lighthouse audits, performance insights, heap
snapshot analysis, extensions, and WebMCP are permanently out of scope.

## Build

```bash
make build    # → dist/chrome-pilot-mcp
make test     # go test ./...
```

## Usage

```bash
chrome-pilot-mcp --version   # print version
chrome-pilot-mcp             # serve MCP over stdio (used by MCP clients)
```

MCP client configuration (once tools land):

```json
{
  "mcpServers": {
    "chrome-pilot": {
      "command": "/path/to/chrome-pilot-mcp"
    }
  }
}
```

## Attribution

Tool names, schemas, and behavior are inspired by
[ChromeDevTools/chrome-devtools-mcp](https://github.com/ChromeDevTools/chrome-devtools-mcp)
(Google LLC, Apache-2.0). This project is an independent clean
reimplementation and contains no code from the upstream.

## License

MIT — see [LICENSE](LICENSE).
