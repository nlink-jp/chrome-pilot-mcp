# Changelog

## [Unreleased]

### Added

- Phase 2 tools, completing the 27-tool surface: input
  (`click`, `click_at`, `drag`, `fill`, `fill_form`, `hover`, `press_key`,
  `type_text`, `upload_file`, `handle_dialog`), console
  (`list_console_messages`, `get_console_message`), network
  (`list_network_requests`, `get_network_request`), emulation
  (`emulate`, `resize_page`), and screencast
  (`screencast_start`, `screencast_stop`) with stdlib animated-GIF assembly
- Phase 1 core tools: `list_pages`, `new_page`, `select_page`, `close_page`,
  `navigate_page`, `wait_for`, `evaluate_script`, `take_snapshot` (a11y uid
  tree with char budget), `take_screenshot` (workspace file + inline image
  content), verified E2E against a real headless Chrome
- In-house RFC 6455 WebSocket client (stdlib only, ws:// loopback)
- CDP client layer: id correlation, flattened sessionId routing, event
  fan-out, typed protocol errors
- Chrome launcher/attach: per-OS/channel executable discovery, ephemeral
  loopback debugging port, dedicated temp profile, loopback-only attach
- CLI flags: `--headless`, `--channel`, `--executable-path`, `--attach`,
  `--workspace-root`, `--viewport`
- Project scaffold: MCP stdio server skeleton (initialize / tools/list /
  tools/call routing, structured tool errors, rich content blocks), ported
  from data-toolbox-mcp
- CLI entry point on stdlib `flag` (zero-dependency policy): default `serve`,
  `version` subcommand and `--version` flag with identical output
- Makefile (build → `dist/`, cross-compile matrix, codesign/notarize wiring)
- RFP (ja/en) under `docs/`
