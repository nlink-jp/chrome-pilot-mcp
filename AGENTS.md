# AGENTS.md — chrome-pilot-mcp

## Project summary

Chrome automation MCP server in Go with no third-party dependencies (the one
required module, nlink-jp/pathguard, is this organization's own).
Reimplements the core automation surface (27 tools) of
ChromeDevTools/chrome-devtools-mcp by speaking CDP (Chrome DevTools Protocol)
directly over WebSocket. Raison d'être: eliminate npm supply-chain risk —
single static binary, no third-party module in `go.mod` (only
nlink-jp/pathguard, itself standard library only), nothing downloaded at
runtime.

**Current stage: released (the version is in `git tag` and the CHANGELOG), 27/27 tools.** All tools verified E2E
against real headless Chrome, plus config.toml, profile persistence, and
host-filter enforcement. v0.2.0 came out of using v0.1.0 as an MCP client,
v0.3.0 out of an external test report — see the CHANGELOG. Both rounds
found defects the unit and scripted-E2E suites had passed, so **use the
thing as a client before calling a release good**.

Design background: `docs/en/chrome-pilot-mcp-rfp.md` (ja: `docs/ja/`) and
the ADRs — 0001 host allow/block lists, 0002 config.toml, 0003 browser
profiles, 0004 per-call workspace root (superseded the same day by 0005), 0005 the
work_dir contract, 0006 writes and uploads under work_dir, 0007 pathguard, 0008
file:// only inside the call's work_dir (with its accepted residual risks).

## Build & test

```bash
make build    # → dist/chrome-pilot-mcp (never `go build` directly)
make test     # go test ./...
make test-linux # same suite on Linux (container)
make package  # release archives (zip/tar.gz) + notarization
make verify-release  # gate: notarized, fresh, runs at this version, clean linux archives (run before upload)
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
                            #   instructions.go = initialize `instructions` text
scripts/                    # codesign/notarize/brew (shared org scripts)
docs/{en,ja}/               # RFP; en has no suffix, ja uses *.ja.md
```

## Gotchas

- **No-third-party-dependency policy is load-bearing.** Never add a Go module
  from outside the nlink-jp organization — not cobra, not a websocket library,
  nothing. This is the project's reason to exist; see CLAUDE.md.
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
- The output directory is the caller's, named on every call and required
  (ADR-0005, organization ADR-021): `take_screenshot` / `screencast_start` /
  the debug tools take `work_dir`, resolved by `Manager.workDir` (argument,
  then the request's `_meta`, then an error). There is no server workspace
  and no launch flag to fall back to. A new file-producing tool must take
  the argument too — an agent confined to its own directories cannot open
  what lands anywhere else, and a path it cannot open is not a result.
  Validate where the argument arrives, not where the file is written.
- Every file argument stays under `work_dir` (ADR-0006, ADR-021 §7), and the
  rules live in one place, `internal/tools/confine.go`: `outputUnder` for a
  file this server writes (`screencast_start`'s `filePath`), `inputUnder` for a
  file it hands to a page (`upload_file`, which therefore takes `work_dir`
  too), and `writeUnder` for every write. What stays refused inside
  `work_dir` (the credential and agent-control places and this server's own
  places — a `work_dir` may be their parent) is nlink-jp/pathguard's judgement
  (ADR-0007): writes under its Local policy (`Resolver.LocalPath`), an upload
  under its Outbound policy (`Resolver.OutboundPath`), because a page can send
  the file anywhere. `writeUnder` is temp file + rename through an
  `os.Root`, so a symlink out of `work_dir` is refused and a hard link is
  replaced, not written through. The screencast opens its root at start and
  holds it to stop. A new tool taking a file path goes through these — a path
  argument checked only for a suffix or for existence is how both gaps got
  in, with tests pinning them. Refusals are `path_not_allowed` with
  `details.reason` (`outside_work_dir` / `sensitive_path` / `server_dir` /
  `browser_profile` / `unresolvable_path` — a chain of links that does not
  end, or a path longer than any system opens / `home_unknown` /
  `unconfigured` — pathguard refuses every call rather than protect
  nothing). `work_dir_denied` carries the same reasons (not
  `outside_work_dir`) plus `system_dir` and `home_dir`.
  pathguard compares places by file identity and by folded name, never by
  name alone: this disk is case-insensitive, and two reviews in a row got past
  a name comparison. Temporary files get a random fixed-length name and
  `O_EXCL` (`createExclusive`). The protected places are one list,
  `Manager.protectedPlaces` (server dir, the driven Chrome's profile kept in
  `profileDir` at connect, every `chrome-pilot-mcp-profile-*` in the temp
  directory, the user's own Chrome roots), and `Manager.resolver` builds a
  resolver from it at the moment of use — the places change while the server
  runs. Tools call `Manager.workDir`, so a `work_dir` inside one is denied too.
  The after-creation check in `writeUnder` (the directory the root reached)
  guards a race no deterministic test can provoke.
- Local files follow ADR-0008, and the rule lives in `internal/tools/localfiles.go`
  (`judgeLocal`): a `file://` (or `view-source:file://`) opens only inside a
  `work_dir` a call named and through pathguard's Local policy. Three places
  apply it — `checkLocalURL` (the `navigate_page` / `new_page` arguments),
  `fileRequestAllowed` (the Fetch interception, always on with `file://*` when
  no host list is set; it sees JavaScript navigation, subresources, iframes and
  view-source) and `refuseUngrantedLocal` (in `selectedPage`, **before**
  attaching, and in the three tools that bypass `selectedPage`:
  `get_network_request`, `get_console_message`, `handle_dialog`). A tab a
  script opened with `window.open` loads before it is attached, which is why
  the reads are refused at their entrance too. Grants are per session
  (`m.grants`, dropped on `close_page`). **Nothing in that decision may take
  `m.mu`**: tool calls hold `m.mu` across CDP calls, and one waiting on a
  paused `file://` load deadlocks — `protectedPlaces` reads `profileDir` under
  `placesMu`; `TestTheInterceptionDoesNotWaitOnTheManagerLock` guards it (and
  the mutant that takes `m.mu` hangs the suite). A new tool that reads page
  state without `selectedPage` must call `refuseUngrantedSession`.
  `navigate_page` / `new_page` declare `work_dir` without requiring it — the
  one exception in `workdir_contract_test.go`, pinned by
  `TestLocalFileWorkDirHasNoDefault`.
- The initialize `instructions` string is `tools.Instructions`
  (`internal/tools/instructions.go`), set on the server by `cmd/root.go`
  `serve` via `srv.SetInstructions`. It is the first thing a model reads, so
  it states the work-dir contract; there is no usage tool, so it points at
  `list_pages` and `take_snapshot`. `workdir_contract_test.go` checks it
  against the registered tools: every tool whose schema declares `work_dir`
  must be named, and every snake_case / lowerCamelCase word in it must be a
  registered tool or a declared argument. Adding or renaming a tool can
  therefore fail there — fix the text, not the test.
- Config precedence lives in `cmd.buildConfig`: only flags reported by
  `flag.Visit` override the file, so an unset flag's zero value can never
  clobber a configured setting. Never search `./config.toml` (ADR-0002).
- Host filtering must stay startup-only: no tool may widen it at run
  time, or a prompt-injected agent could unlock itself.
- Any CDP call that needs the renderer must go through `rendererCall` /
  `callGuarded` (dialogguard.go): alert/confirm/prompt block the renderer,
  so an unguarded call waits out its whole timeout. Guarding only the input
  dispatch was not enough — v0.3.0 still hung for 30s in DOM.getBoxModel,
  which runs before the click. Add a new renderer-facing call and you must
  route it through the guard too.
- The diagnostic tools deliberately do NOT touch the renderer: list_pages
  and the console/network readers answer from collector state, so they keep
  working while a dialog blocks the page. Keep it that way — inspecting a
  frozen page is when they matter most.
- The accessibility tree is not the whole page. Unnamed clickable elements
  only reach the agent through the DOM pass in extranodes.go; keep the
  uid map able to hold a nodeId as well as a backendNodeId.
- `emulate` declares the whole emulation state: adding a dimension means
  resetting it when omitted AND reporting it in `applied`. Reporting only
  the parameters that were passed once made an unreset user agent look
  cleared.
- Limits must not be enforced only where events arrive. maxDurationMs was
  checked on frame arrival, so a page that stopped painting never hit it;
  wall-clock limits need a timer.
- Never trust an event as the sole completion signal. Navigation falls back
  to `document.readyState` because a missed load event was reported as a
  failed navigation for a page that had loaded.
- The resolver is built in exactly one place, `Manager.resolver()` in
  `internal/tools/manager.go`, from `Manager.protectedPlaces`, at every use:
  `Manager.workDir` for `work_dir`, and the file checks in `confine.go`. The
  places include `config.Dir()` (organization ADR-021 §4) — the one
  expression for this server's own tree, which holds both `config.toml` and
  `browser.ProfilesDir()`'s managed browser profiles, so the refusal covers
  the cookies and logged-in sessions those profiles accumulate. Do not build
  a `workdir.Resolver` anywhere else, and do not keep one: a resolver without
  the places lets a caller drop screenshots into a live browser profile or
  over the config that governs what the server may launch, and a kept one
  misses a throwaway profile that appeared after it was built
  (`TestAThrowawayProfileCreatedDuringARecordingIsProtected`).
