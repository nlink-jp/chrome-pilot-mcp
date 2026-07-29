# RFP: chrome-pilot-mcp

> Generated: 2026-07-29
> Status: Draft

## 1. Problem Statement

Provide browser automation to MCP clients (Claude Code, etc.) while
eliminating the supply-chain risk inherent in the npm ecosystem.

The upstream [chrome-devtools-mcp](https://github.com/ChromeDevTools/chrome-devtools-mcp)
(Google, Apache-2.0) runs via npx, carries a large transitive dependency
tree (puppeteer / lighthouse / chrome-devtools-frontend), and puppeteer
downloads browser binaries at runtime. Given the current state of npm
supply-chain attacks, adopting this form inside the organization carries
non-trivial risk.

chrome-pilot-mcp reimplements its core automation surface (27 tools) as a
**single Go binary with zero external dependencies (stdlib only)**. It
speaks the Chrome DevTools Protocol (CDP) directly over WebSocket, fetches
nothing at runtime, and only launches or attaches to an already-installed
Chrome. Target users are MCP client users inside the nlink-jp org who need
browser automation.

## 2. Functional Specification

### Commands / API Surface

MCP server (stdio transport) exposing 27 tools:

| Category | Tools |
|---|---|
| Pages (6) | `list_pages` / `new_page` / `select_page` / `close_page` / `navigate_page` / `wait_for` |
| Input (10) | `click` / `click_at` / `drag` / `fill` / `fill_form` / `hover` / `press_key` / `type_text` / `upload_file` / `handle_dialog` |
| Debugging (5) | `take_snapshot` / `take_screenshot` / `evaluate_script` / `list_console_messages` / `get_console_message` |
| Network (2) | `list_network_requests` / `get_network_request` |
| Emulation (2) | `emulate` / `resize_page` |
| Screencast (2) | `screencast_start` / `screencast_stop` (animated GIF output) |

Tool names and argument schemas follow the upstream core automation tools
so agents can reuse existing usage patterns as-is.

CLI flags (server launch options):

- `--headless` — launch Chrome headless
- `--channel <stable|beta|dev|canary>` — Chrome channel to launch
- `--executable-path <path>` — explicit Chrome binary
- `--attach <ws://...|port>` — attach to an existing Chrome debugging
  endpoint (no self-launch)
- `--workspace-root <dir>` — output directory for screenshots / GIFs
- `--viewport <WxH>` — initial viewport
- `--version` — print version (required responder; brew test invokes it)

### Input / Output

- MCP (JSON-RPC 2.0 over stdio).
- Large outputs are file-mediated: screenshots and GIFs are written under
  the workspace and returned as paths. Screenshots are additionally
  returned as MCP image content so clients can render them directly.
- A11y snapshots, console and network listings are text with a character
  budget, truncated at build time.

### Configuration

CLI flags only. No config file, no environment variables (no secrets are
handled).

> **Updated 2026-07-29**: ADR-0002 adds config.toml support (precedence
> flags > config.toml > defaults). This section is superseded by that ADR.

### External Dependencies

- **Go module dependencies: zero** (stdlib only). The WebSocket client
  (RFC 6455, localhost / plaintext only) is written in-house.
- **Runtime dependency: the user's installed Chrome only.** Nothing is
  ever downloaded.

## 3. Design Decisions

- **Go**: single binary, cross-compilation, the org's standard stack. The
  MCP server skeleton (transport/jsonrpc/mcpserver/toolerr) is ported from
  data-toolbox-mcp (the org's scaffold convention for new MCP servers).
- **Zero dependencies (stdlib only)**: supply-chain risk elimination is
  the project's raison d'être, so the Go side carries no external modules
  either. The only stdlib gap, a WebSocket client, is a minimal in-house
  implementation (~300 lines) restricted to CDP's needs (localhost,
  plaintext, no compression).
- **Direct CDP**: no abstraction layer such as puppeteer or chromedp; use
  the stable CDP domains (Page / DOM / Runtime / Input / Network /
  Accessibility / Emulation) directly. Chrome does the heavy lifting
  (rendering, PNG screenshots, a11y tree construction), so the Go side
  stays a thin protocol layer.
- **Screencast as GIF**: upstream produces video; instead, JPEG/PNG frames
  from CDP `Page.startScreencast` are composited into an animated GIF via
  stdlib `image/gif` (Floyd–Steinberg dithering, 256-color palette).
- **Permanently out of scope**: Lighthouse audits, performance trace
  insights, heap snapshot analysis (12 tools), extensions, WebMCP, 3p
  developer tools. These amount to porting the chrome-devtools-frontend /
  lighthouse engines — several times the effort of the entire core — while
  real agent usage concentrates on core automation.
- **Complements**: follows the same file-mediated workspace model and
  structured tool errors ({code, message}) as the org's other MCP servers
  (data-toolbox-mcp, pcap-analyzer-mcp, etc.).
- **Attribution**: tool names, schemas, and descriptions derive from the
  Apache-2.0 upstream; inspired-by attribution is stated in README /
  NOTICE.

## 4. Development Plan

### Phase 1: Core

- Port the 4 MCP skeleton packages from data-toolbox-mcp
  (`internal/{transport,jsonrpc,mcpserver,toolerr}`)
- In-house WebSocket client (RFC 6455) + CDP client layer (call/event
  dispatch, target/session management)
- Chrome launcher (dedicated user-data-dir, headless support) / attach
- Tools: 6 page tools + `evaluate_script` + `take_snapshot` +
  `take_screenshot`
- Unit test foundation with a fake CDP server (mockable external
  dependencies)

At the end of Phase 1 the project is independently reviewable as a minimal
"navigate, snapshot, screenshot" MCP server.

### Phase 2: Features

- 10 input tools
- console / network / emulation / `wait_for`
- Screencast GIF (with default frame decimation and downscaling)
- E2E harness against a real Chrome (pre-release real-data E2E convention)

### Phase 3: Release

- docs/{en,ja} three-tier docs, README.md / README.ja.md / CHANGELOG.md /
  AGENTS.md
- make build / build-all, signing / notarization, release zips (canonical
  binary name)
- Add as submodule to the util-series umbrella, update the org profile,
  run check-org.sh

## 5. Required API Scopes / Permissions

None — no external services, API keys, or credentials. The server talks
only to a local Chrome process.

## 6. Series Placement

Series: **util-series**

Reason: a general-purpose browser automation MCP server, placed alongside
the org's other MCP servers (data-toolbox-mcp, voice-studio-mcp, etc.).
Neither security-specific (cybersecurity-series) nor experimental
(lab-series).

## 7. External Platform Constraints

- **CDP is not a stable API** (changes at tip-of-tree). Restrict usage to
  the stable domains (Page / DOM / Runtime / Input / Network /
  Accessibility / Emulation); no experimental domains.
- **Behavioral differences across Chrome versions** — document a support
  policy (current stable plus roughly the two preceding versions).
- **`--remote-debugging-port` is a local attack surface** — 127.0.0.1
  bind required, dedicated user-data-dir, ephemeral port; documented as
  part of the security design (security built alongside implementation).
- **A11y snapshot size** — truncated at build time under a character
  budget.
- **GIF screencast limitations** — 256 colors, large files; downscaling
  and frame decimation by default.

---

## Discussion Log

- 2026-07-29: Origin: the supply-chain risk of adopting the npx-run,
  npm-dependency-heavy chrome-devtools-mcp inside the org. Goal: a clean
  reimplementation as a single binary with no external dependencies.
- Surveyed upstream v1.6.0: ~17,000 lines of TS, 52 tools. The dependency
  weight is concentrated in the analysis features (lighthouse /
  chrome-devtools-frontend / puppeteer); core automation is replaceable
  with direct CDP.
- Scope: compared full parity (52) / core automation (~30) / minimal
  (~15); adopted **core automation**. Analysis features are permanently
  out of scope (engine ports, several times the effort).
- Dependency policy: compared allowing one vetted WebSocket library vs.
  stdlib-only; adopted **stdlib only / zero deps** with a minimal in-house
  WebSocket client (~300 lines, localhost/plaintext only).
- Naming: compared browser-pilot-mcp / chrome-pilot-mcp / cdp-mcp; adopted
  **chrome-pilot-mcp** (make the Chrome specificity explicit).
- Connection: **self-launch + attach**, defaulting to self-launch with a
  dedicated user-data-dir; `--attach` covers verification in logged-in
  sessions.
- Output: **file-mediated, with screenshots also returned as MCP image
  content** (org convention + direct client rendering).
- Configuration: **CLI flags only** (no secrets; MCP servers are launched
  from client config args).
- Screencast: initially excluded, restored after the user pointed out that
  stdlib `image/gif` can composite animated GIFs (total: 27 tools).
- Screencast format: compared against APNG (24-bit truecolor, feasible
  with zero deps via in-house chunk assembly); **GIF adopted**. Reasons:
  only GIF animates when attached in Slack (the org is ChatOps-centric),
  full-frame APNG bloats quickly and delta optimization would be a
  substantial implementation, and the recorded content is flat-color,
  text-heavy web UI where GIF's weaknesses rarely show. Adding an opt-in
  `format: "apng"` later is confirmed technically feasible.
