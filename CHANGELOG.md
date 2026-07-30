# Changelog

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
