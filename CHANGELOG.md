# Changelog

## [Unreleased]

### Added

- Project scaffold: MCP stdio server skeleton (initialize / tools/list /
  tools/call routing, structured tool errors, rich content blocks), ported
  from data-toolbox-mcp
- CLI entry point on stdlib `flag` (zero-dependency policy): default `serve`,
  `version` subcommand and `--version` flag with identical output
- Makefile (build → `dist/`, cross-compile matrix, codesign/notarize wiring)
- RFP (ja/en) under `docs/`
