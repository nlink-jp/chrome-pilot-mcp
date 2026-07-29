# chrome-pilot-mcp

> **Status: feature-complete, pre-release.** All 27 tools are implemented
> and verified end-to-end against a real Chrome. Remaining before v0.1.0:
> release packaging. Design background: [RFP](docs/en/chrome-pilot-mcp-rfp.md).

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

## Tool surface (27 tools)

| Category | Tools |
|---|---|
| Pages (6) | `list_pages` / `new_page` / `select_page` / `close_page` / `navigate_page` / `wait_for` |
| Input (10) | `click` / `click_at` / `drag` / `fill` / `fill_form` / `hover` / `press_key` / `type_text` / `upload_file` / `handle_dialog` |
| Debugging (5) | `take_snapshot` / `take_screenshot` / `evaluate_script` / `list_console_messages` / `get_console_message` |
| Network (2) | `list_network_requests` / `get_network_request` |
| Emulation (2) | `emulate` / `resize_page` |
| Screencast (2) | `screencast_start` / `screencast_stop` (animated GIF) |

Notable behaviors:

- Element tools address elements by the `uid`s from `take_snapshot`
  (accessibility tree); each new snapshot invalidates previous uids.
- `take_screenshot` saves to the workspace and returns the image inline
  when small enough; `screencast_stop` writes an animated GIF assembled
  entirely with the Go standard library.
- `drag` is mouse-event based; HTML5 dragstart/drop-based UIs are not
  simulated.
- Console and network capture starts when a page is first touched by a
  tool (domains are enabled on attach).

Tool names and argument schemas follow the upstream so agents can reuse
existing usage patterns. Lighthouse audits, performance insights, heap
snapshot analysis, extensions, and WebMCP are permanently out of scope.

## Install

Homebrew (macOS, Apple Silicon — signed & notarized prebuilt binary):

```sh
brew install nlink-jp/tap/chrome-pilot-mcp
```

Or grab a prebuilt binary for linux/amd64, linux/arm64, darwin/arm64, or
windows/amd64 from the
[releases page](https://github.com/nlink-jp/chrome-pilot-mcp/releases).

To build from source (Go 1.23+):

```sh
git clone https://github.com/nlink-jp/chrome-pilot-mcp
cd chrome-pilot-mcp
make build          # → dist/chrome-pilot-mcp
```

You also need Google Chrome installed — this server drives the browser you
already have and never downloads one.

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

Flags:

| Flag | Meaning |
|---|---|
| `--headless` | Launch Chrome headless |
| `--channel <stable\|beta\|dev\|canary>` | Chrome channel to launch (default stable) |
| `--executable-path <path>` | Explicit Chrome binary (overrides `--channel`) |
| `--attach <ws://…\|port\|host:port>` | Attach to an existing debugging endpoint (loopback only) instead of launching |
| `--workspace-root <dir>` | Output directory for screenshots etc. (default: fresh temp dir) |
| `--viewport <WxH>` | Initial viewport, e.g. `1280x800` |
| `--profile <name>` | Named persistent profile, kept across runs |
| `--user-data-dir <path>` | Explicit user-data-dir (exclusive with `--profile`) |
| `--allow-hosts <list>` | Comma-separated host allow list; setting any switches to default-deny |
| `--block-hosts <list>` | Comma-separated host block list; wins over the allow list |
| `--block-local` | Refuse `file://` and `data:` URLs |
| `--config <path>` | Config file to read (see below) |

Chrome is launched lazily on the first tool call, with the debugging port
bound to `127.0.0.1` on an ephemeral port.

## Configuration file

Everything above can also live in a TOML file — see
[config.example.toml](config.example.toml). `--config <path>` reads that
file (a missing file is a startup error); otherwise
`~/.config/chrome-pilot-mcp/config.toml` is used when present. The
working directory is **never** searched, so a config cannot be injected by
whatever directory the server happens to start in. Explicitly given flags
override the file.

```toml
[browser]
headless = true
profile  = "work"

[security]
allow_hosts = ["example.com", "*.example.com"]
```

## Browser profiles

By default each run gets a throwaway profile that is deleted on shutdown.
`--profile <name>` keeps one under
`~/.config/chrome-pilot-mcp/profiles/<name>` (mode 0700) so logins survive
across runs; `--user-data-dir <path>` uses a directory you name. Both are
refused together with `--attach`, and pointing `--user-data-dir` at your
real Chrome profile is refused outright — use `--attach` to drive a
browser you already have open.

A persistent profile accumulates cookies and logins. Treat that directory
as sensitive.

## Restricting where the browser can go

An agent driving this server can be prompt-injected, so destinations can
be limited from outside the agent's reach: the lists are fixed at startup
and no tool can change them.

```bash
chrome-pilot-mcp --allow-hosts "example.com,*.example.com"
```

Setting any `--allow-hosts` entry switches to default-deny.
`*.example.com` matches subdomains only, so list the apex separately if
you need it. `--block-hosts` wins over the allow list and works on its own
as a denylist.

Enforcement is two-layer: `navigate_page` / `new_page` refuse up front
with a `host_not_allowed` error, and a CDP interception fails every other
request — in-page `fetch`, redirects, subresources — with
`BlockedByClient`. Blocked requests still show up in
`list_network_requests`, so a blocked load is visible rather than
mysterious. With no lists configured the interception is never installed.

Known gaps (see [ADR-0001](docs/en/adr/0001-host-allow-block-lists.md)):
WebSocket connections are not intercepted, and pages the tool never
attached to are not covered. This is a guardrail against agent mishaps,
not a substitute for OS-level egress control.

MCP client configuration:

```json
{
  "mcpServers": {
    "chrome-pilot": {
      "command": "/path/to/chrome-pilot-mcp",
      "args": ["--headless"]
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
