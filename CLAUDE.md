# CLAUDE.md — chrome-pilot-mcp

Read AGENTS.md for project summary, build commands, and structure.

## Non-negotiable project rules

1. **Zero external dependencies.** `go.mod` must never gain a `require`
   line. No cobra, no websocket libraries, no CDP libraries (chromedp etc.),
   no image libraries. stdlib only — supply-chain risk elimination is this
   project's reason to exist. If a need seems to require a dependency,
   stop and discuss instead of adding it.
2. **CDP stable domains only**: Page / DOM / Runtime / Input / Network /
   Accessibility / Emulation. No experimental domains.
3. **Nothing is ever downloaded at runtime.** The binary launches or
   attaches to the user's installed Chrome; it must never fetch browsers,
   lists, or updates from the network.
4. **Security posture**: Chrome is always launched with `127.0.0.1`-bound
   remote debugging on an ephemeral port and a dedicated user-data-dir.
   Do not weaken these defaults.
5. **Analysis features are permanently out of scope** (Lighthouse,
   performance insights, heap snapshots, extensions, WebMCP). Do not add
   them "while at it".
6. **MCP owns stdout** — logs go to stderr only.

Org-wide rules (tests mandatory, `make build` not `go build`, docs in sync,
typed commits) apply as usual; see the workspace CLAUDE.md and
CONVENTIONS.md.
