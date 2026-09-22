# Changelog

## [0.10.0] - 2026-09-22

### Security

- **A `file://` URL read any file the process could read** (v0.9.0 and
  earlier, without `--block-local`): `navigate_page` or `new_page` to
  `file:///<home>/.ssh/id_rsa`, then `take_snapshot` or `evaluate_script`.
  So did `view-source:file://`, a script in an opened local page navigating to
  another local file, and a tab it opened with `window.open` (ADR-0008;
  measured with headless Chrome before the fix).

### Changed — behaviour change

- **A local file opens only under the call's `work_dir`** (ADR-0008).
  `navigate_page` and `new_page` take `work_dir` for a `file://` URL
  (`view-source:file://` too) — the argument, else the runtime's `_meta` hint,
  else `work_dir_required` — and refuse a file outside it, or a credential or
  agent-control location under it, with `path_not_allowed`. A local file
  anywhere else no longer opens.
- A page may load local files — subresources, frames, a script's
  navigation — only from under the `work_dir`s its session was opened with;
  anything else fails inside Chrome (`BlockedByClient`). The CDP interception
  is therefore always on: with no host list it covers `file://` only, which
  pauses no http request (measured).
- A tab showing a local file no call opened (a `window.open` tab, one the user
  opened) can only be navigated away with `navigate_page`; other tools refuse
  it with `path_not_allowed`, before attaching to it. `get_network_request` and
  `get_console_message` refuse records from such a page, and the body of a
  local-file load no call opened; `handle_dialog` still answers a dialog there
  but withholds its text.
- A URL containing control characters is refused (Chrome drops tabs and
  newlines before parsing, so `fi\tle:///…` is a local file to it).
- `view-source:` URLs are judged by the URL inside them, by the host lists
  too.
- ADR-0001's note that `file://` is enforced at the tool-argument layer only is
  corrected: the interception does see local files.

## [0.9.0] - 2026-09-22

### Changed

- **Path judgement moved to [nlink-jp/pathguard](https://github.com/nlink-jp/pathguard)**
  (ADR-0007): the work-directory check, the refused places for writes and
  uploads, and the identity comparison this server had of its own
  (`refusedLocation`, `insideByIdentity`). `go.mod` now requires that one
  module of this organization, which itself has no dependency; CLAUDE.md's
  dependency rule says so.
- `upload_file` is judged as a file that **leaves the machine** (pathguard's
  Outbound policy): a file named as a secret (`id_rsa`, `credentials.json`,
  `*service-account*.json`, `.env`) or whose path passes through a credential
  directory or file name (`.ssh`, `.aws`, `.config/gcloud`, `.npmrc`,
  `.netrc`, `.git-credentials`, `.bash_history`, `.docker/config.json` and
  the like) is now refused wherever it sits, even inside `work_dir` and
  outside your home.
- `work_dir` is judged by pathguard's list of what may not be a work
  directory, and writes by its Local policy: the real places under your home
  from the list gem-agent and lagent use are refused (newly `~/.kube`,
  `~/.config/gh`, `~/.azure`, `~/.terraform.d`, `~/.gemini`,
  `~/.config/mcp-bridge`, `~/.netrc`, `~/.npmrc`, `~/.pypirc`,
  `~/.git-credentials`, `~/.vault-token`, `~/.docker/config.json`,
  `~/.claude.json`, `~/.bash_history`, `~/.zsh_history`), under every spelling,
  and wherever a link directly inside one of those directories points. When
  `$HOME` names another directory than the account's home, those places are
  protected under both; this server's own directory follows `$HOME`, as it
  did before, and on macOS and Linux your own Chrome profiles are protected
  under `$HOME` (when it is absolute) and under the account's home.
  `.env.example`, `.env.sample`, `.env.template` and `.env.dist` are
  accepted. When the home directory cannot be determined, every call is
  refused.
- A path with a NUL byte, and an upload whose path cannot be resolved — a
  chain of links that does not end, a path longer than any system opens — are
  refused with `details.reason` `unresolvable_path` (a write through a chain
  of links that does not end is refused as `outside_work_dir`).
- `work_dir_denied` carries `reason` in its `details`.
- `upload_file`'s `filePath` description names the refusals above.

### Fixed

- A write whose directory is reached through a chain of links passing through
  a credential directory (`screenshots/` → `~/.config/gcloud/sub/link` → a
  directory in `work_dir`) is refused: only the end of the chain was judged.
- An upload is judged before its links are resolved, so a credential file
  that does not exist is refused like one that does, instead of being
  reported as missing — which told the caller which secrets are there.
- An upload outside `work_dir` is refused as outside whether or not the file
  exists; a missing one was reported with the file-system error, which also
  named the first missing directory. (A chain of links that does not end is
  refused as `unresolvable_path` wherever it is.)
- On macOS and Linux your own Chrome profiles are protected under the
  account's home as well as `$HOME` — for the file checks and for the refusal
  to drive your own profile: with `$HOME` pointed elsewhere, or relative, they
  were not protected. (Linux release builds read the account's home from
  `/etc/passwd`; for a directory-service account it stays `$HOME`.)

## [0.8.0] - 2026-09-22

### Security

- **Files stay under `work_dir`, in both directions** (ADR-0006, organization
  ADR-021 §7). `screencast_start`'s `filePath` was checked only for a `.gif`
  suffix: it could name any path the process could write, creating directories
  and overwriting files. `upload_file` handed Chrome any readable file,
  directories included, without consulting the credential blacklist — and a
  page can send what it is given anywhere. Tests pinned both behaviours.
- **Protected places inside `work_dir` are refused too**: the credential
  blacklist, this server's own directory (`config.toml` and the managed
  profiles), the profile of the Chrome it drives, every throwaway
  `chrome-pilot-mcp-profile-*` in the temp directory, and your own Chrome
  profiles. A `work_dir` may be a parent of any of them; the files below would
  then be live cookies. They are compared by file identity, not by name — the
  disk is case-insensitive, and a name comparison let
  `CHROME-PILOT-MCP/profiles/…/Cookies` through during review.
- **Every write goes through one path**: an `os.Root` on `work_dir` (a link out
  of it is refused), a check of where the directory would be before anything is
  created and again after, and a random, exclusively created temporary file
  renamed into place — so a hard link in `work_dir` to a file outside is
  replaced, not written through. `take_screenshot` included.

### Changed

- **Breaking: `upload_file` takes a required `work_dir`** and hands the page
  only a file under it (relative to it, or absolute inside it). Copy a file into
  your work directory first. A runtime that sets `_meta["jp.nlink/work_dir"]`
  needs no change.
- **Breaking: a screencast `filePath` must lie under `work_dir`** — relative to
  it (it used to be relative to the server's own working directory) or absolute
  inside it. Anything else is refused at `screencast_start`, before recording.
- A refusal is `path_not_allowed` with `details.reason`: `outside_work_dir`,
  `sensitive_path`, `server_dir` or `browser_profile`. A `work_dir` inside a
  protected place is `work_dir_denied`, whatever the spelling.
- `upload_file` reports the file's real path in `uploaded` — the path Chrome
  was given; for a symlink, the page sees the target's name.
- A `work_dir` swapped or moved while a recording runs is refused at stop
  rather than followed. A `screencasts/` or `screenshots/` that is an absolute
  symlink fails (`os.Root` refuses absolute links); a relative one inside
  `work_dir` works. Written files are mode `0644`.
- Building from source needs Go 1.25 (`os.Root.MkdirAll`).

## [0.7.0] - 2026-09-22

### Added

- **The initialize reply now carries `instructions`**, the hint a client hands
  to its model before any tool list. It says what the server is for, to start
  with `list_pages` and `take_snapshot`, that `take_screenshot` and
  `screencast_start` require an absolute `work_dir` with no default and where
  their files land, and that an open dialog waits for `handle_dialog`. Every
  other work-dir server in the org already sent one (organization ADR-021).
- Tests pin it: the initialize result carries what `SetInstructions` was given
  and omits the key when nothing was; the text states the work-dir contract and
  names every tool whose schema declares `work_dir`; every code name in it is a
  registered tool or a declared argument; no retired work-dir name appears; and
  the served binary sets it.

### Fixed

- **`make verify-release` now fails closed.** Its last block chained unzip, the
  packaged binary's `--version` and `spctl` with `&&` and ended the whole chain
  in `|| true`, so a zip that did not unpack or a binary that did not run exited
  0 and the upload proceeded. Each step is now judged on its own, the packaged
  binary's `--version` must contain the tag being released, and only the
  informational `spctl` line may be ignored. Matches the org template
  (CONVENTIONS.md §Code Signing → Verifying a release).

### Tests

- The per-tool contract tests fail when no tool is registered. They loop over
  the registered tools, and with an empty list every one of them passed without
  examining anything.

## [0.6.0] - 2026-09-21

### Security

- **`work_dir` may no longer be this server's own directory.** Organization
  ADR-021 §4 closes the work-directory checks with "not a system location …
  and not the server's own config or state directory" → `work_dir_denied`,
  and the resolver has carried a `Denied` list for exactly that — but nothing
  populated it, so it ran as its zero value. That tree is both the config and
  the state directory here: it holds `config.toml`, which can name an
  executable to launch and widen the host limits, and `profiles/`, the
  managed browser profiles with their cookies and logged-in sessions. A
  caller could name it as its `work_dir` and have the server write
  screenshots, PDFs and GIFs into a live browser profile or over that config,
  on a model's say-so. `config.Dir()` —
  `~/Library/Application Support/chrome-pilot-mcp` on macOS,
  `~/.config/chrome-pilot-mcp` on Linux — and everything under it is now
  refused.
- `config.Dir()` is a new single expression for that tree; `config.DefaultPath`
  and `browser.ProfilesDir` both derive from it, so the denial cannot drift
  away from the location it protects.
- `resolveWorkDir`'s doc comment no longer describes a server default that
  ADR-021 removed.

## [0.5.2] - 2026-09-14

### Added

- `TestEveryRequiredNameIsDeclared` — a schema that lists a name in `required`
  without declaring it in `properties` makes a strict client refuse the whole
  tool list (Vertex AI: "schema at top-level requires unspecified property").
  data-toolbox-mcp shipped exactly that and broke a session outright; the
  existing contract test checked declared ⇒ required only, so the fleet is
  pinned in both directions now.

## [0.5.1] - 2026-09-13

### Fixed

- **`take_screenshot` and `screencast_start` declared `work_dir` without
  requiring it.** The handlers have required it since 0.5.0, so the schema told
  the model the argument was optional and the call then failed. Both schemas
  mark it required, and `take_screenshot`'s description names the `work_dir`
  rather than "the workspace".
- The RFP still listed `--workspace-root` among the flags; it is annotated as
  withdrawn, in both languages.

### Added

- A contract test walking every registered tool: no retired spelling in a
  schema *or* a description, `work_dir` required wherever it is declared, and
  the two path-returning tools declaring it at all. ADR-0005 asked for this
  test; the release shipped without it, which is why the missing `required`
  went out.

## [0.5.0] - 2026-09-13

### Changed

- **Breaking: `workspaceRoot` is now `work_dir`, and it is required.** Every
  file-producing tool (`take_screenshot`, `screencast_start`, the debug tools)
  names the directory it writes into, and there is no default to fall back to.
  See [ADR-0005](docs/en/adr/0005-work-dir-contract.md), which amends ADR-0004
  the same day; organization ADR-021 settles the spelling fleet-wide.
- **Breaking: the `--workspace-root` flag and the `[workspace] root` config key
  are removed**, along with the temp-directory fallback. A config still carrying
  the key fails at startup with the reason named. A call written for upstream
  chrome-devtools-mcp now has to name `work_dir` too — the error says so.
- The work directory must already exist; the server no longer creates it. A path
  that is not there is a typo, and creating it silently would put the file
  somewhere the caller is not looking.
- A runtime may supply the directory instead of the model: the server reads
  `_meta["jp.nlink/work_dir"]` when the argument is absent. The argument wins.

### Added

- `work_dir_required`, `work_dir_invalid`, `work_dir_not_found`,
  `work_dir_not_writable`, `work_dir_denied` — the fleet's codes for the part of
  the contract that failed.

## [0.4.0] - 2026-09-13

### Added

- **`take_screenshot` and `screencast_start` accept an optional absolute
  `workspaceRoot`** and write under it instead of the server's own workspace
  (ADR-0004). The workspace root was chosen once at startup, while the agent
  that has to open the result is confined to its own directories — gem-agent,
  lagent and Claude Code all refuse a path outside the project and the
  session directory, so a screenshot written to the server's temp directory
  came back as a path the caller could not read. The directory is created if
  missing, a relative path or `~` is refused (nothing expands them inside a
  JSON argument), an explicit `filePath` on `screencast_start` still wins,
  and calls that omit it behave exactly as before.

## [0.3.1] - 2026-07-30

### Fixed

- **Every renderer-facing call now fails fast while a JavaScript dialog is
  open.** v0.3.0 guarded input dispatch and `evaluate_script`, but a click
  first resolves the element's geometry, and `DOM.getBoxModel` blocks on an
  open dialog just as hard — clicking with a dialog already open waited out
  the full 30s timeout before the guarded dispatch was ever reached. DOM
  geometry and node resolution, the accessibility tree, screenshots, script
  evaluation and file input all go through the guard now and return a
  `dialog_open` error naming the dialog and pointing at `handle_dialog`
  (0.00s in place of 30s against real Chrome).

  A dialog opened *by* an action still comes back as the `dialogOpen` note
  on a successful result — the action landed. A dialog that was already open
  is a precondition failure, so it is an error.

## [0.3.0] - 2026-07-30

From an external test report covering all 27 tools.

### Fixed

- **`emulate` now clears the user-agent override when it is omitted.** Every
  other dimension was reset by a bare `emulate {}`, but a user agent set
  earlier survived — and the response dropped the `userAgent` key, so the
  reset looked like it had happened. Requests kept going out with the stale
  UA. `applied` now names every dimension and its effective value, so a
  reset is never ambiguous
- **`screencast_start`'s `maxDurationMs` is enforced by a deadline.** The
  limit was only checked when a frame arrived, so a page that stopped
  repainting never hit it and recorded past the budget. `screencast_stop`
  now always reports `truncated` (with `truncatedBy` when true), and
  separates `recordedMs` (wall-clock span of the frames) from
  `gifDurationMs` (how long the GIF plays)
- **`take_snapshot` reports `checked` and `disabled`.** A checkbox rendered
  identically whether or not it was ticked, so verifying a toggle required
  `evaluate_script`. `disabled=true` is reported too, since it explains an
  element that does not respond
- Navigation no longer reports a timeout for a page that did load: if the
  load event is missed, `document.readyState` decides, and the result
  carries a note

### Changed

- `drag`'s description was wrong. Chrome turns the mouse sequence into a
  native drag, so HTML5 `draggable` elements and `ondrop` handlers **do**
  fire — verified against a plain div. Only DnD built on raw pointer events
  with its own thresholds may need finer steps
- `emulate`'s `extraHttpHeaders` documents its shape (`{"X-Custom":"value"}`)
  and the error message shows it; the parameter remains the one dimension an
  omitted value leaves unchanged
- `emulate` notes that clearing `Offline` makes Chrome reload the error page
  on its own, so a URL that failed while offline can end up looking loaded
- The "no frames captured" error explains that Chrome only emits frames when
  the page repaints, and names the limit that ended the recording

## [0.2.0] - 2026-07-30

Everything here came out of driving v0.1.0 as a real MCP client.

### Fixed

- **Clickable elements the accessibility tree omits are now reachable.**
  An element with a click handler but no text, aria-label or role (an
  icon-only button, an empty click target) never appears in
  `Accessibility.getFullAXTree`, so `take_snapshot` never showed it and no
  uid could address it. A DOM pass now recovers those elements in three
  CDP calls and lists them below the tree
- **A click that opens a dialog no longer looks like a failure.**
  alert/confirm/prompt block the renderer, so the CDP call that triggered
  one never replies and the click came back as a 30s timeout even though
  it had worked. Input dispatch and `evaluate_script` now return as soon
  as the dialog opens, reporting it and pointing at `handle_dialog`
  (0.13s instead of 30s)
- **Resizing during a screencast no longer discards the recording.**
  Frames that did not match the first frame's size were dropped, so a
  resize mid-recording left a single-frame GIF. Frames are composited onto
  the largest frame's canvas instead, and the result reports
  `refittedFrames`

### Added

- `wait_for` accepts `selector` plus `state` (`visible`, `hidden`,
  `present`, `absent`) alongside the existing text matching — waiting for
  a spinner to disappear no longer needs a text proxy
- `list_console_messages` and `list_network_requests` return `lastMsgId` /
  `lastReqId` and accept `sinceMsgId` / `sinceReqId`, so a step can ask
  only for what happened since the previous call
- `list_network_requests` gains `failedOnly` (failures and status >= 400)
- `screencast_start` gains `maxFrames` and `maxDurationMs`; memory is
  bounded by bytes rather than a frame count, and a truncated recording
  reports which limit stopped it
- `screencast_stop` reports the output `width`/`height`

## [0.1.0] - 2026-07-29

Initial release. A zero-dependency Chrome automation MCP server: a single
Go binary with no external modules, speaking the Chrome DevTools Protocol
directly and driving the Chrome you already have installed.

### Added

- **27 tools** covering the core browser automation surface:
  - Pages: `list_pages`, `new_page`, `select_page`, `close_page`,
    `navigate_page`, `wait_for`
  - Input: `click`, `click_at`, `drag`, `fill`, `fill_form`, `hover`,
    `press_key`, `type_text`, `upload_file`, `handle_dialog`
  - Debugging: `take_snapshot` (accessibility tree with uids),
    `take_screenshot` (workspace file plus inline image),
    `evaluate_script`, `list_console_messages`, `get_console_message`
  - Network: `list_network_requests`, `get_network_request`
  - Emulation: `emulate`, `resize_page`
  - Screencast: `screencast_start`, `screencast_stop`, producing an
    animated GIF assembled with the Go standard library
- **Host allow/block lists** (ADR-0001) to bound where an agent can send
  the browser: `--allow-hosts` (setting any switches to default-deny),
  `--block-hosts` (wins over allow), `--block-local`. Enforced at the tool
  arguments (`host_not_allowed`) and again at the CDP layer, so in-page
  fetch, redirects, and subresources are covered. No interception is
  installed when unconfigured
- **config.toml** (ADR-0002): `--config <path>`, else
  `~/.config/chrome-pilot-mcp/config.toml`. The working directory is never
  searched. Explicitly given flags override the file. See
  `config.example.toml`
- **Browser profiles** (ADR-0003): `--profile <name>` for a persistent
  profile under the tool's managed directory (mode 0700),
  `--user-data-dir` for an explicit path; the default remains a throwaway
  profile per run. Conflicting options are refused, as is driving the
  user's real Chrome profile
- Chrome is launched lazily with the debugging port bound to `127.0.0.1`
  on an ephemeral port, or `--attach`ed to an existing loopback endpoint.
  Nothing is downloaded at run time

[0.3.1]: https://github.com/nlink-jp/chrome-pilot-mcp/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/nlink-jp/chrome-pilot-mcp/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/nlink-jp/chrome-pilot-mcp/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/nlink-jp/chrome-pilot-mcp/releases/tag/v0.1.0
