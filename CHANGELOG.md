# Changelog

## [Unreleased]

### Added

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
