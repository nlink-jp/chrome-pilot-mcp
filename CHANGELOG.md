# Changelog

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

[0.1.0]: https://github.com/nlink-jp/chrome-pilot-mcp/releases/tag/v0.1.0
