# ADR-0001: Navigation restriction via host allow/block lists

- Status: Proposed
- Date: 2026-07-29
- Driver: magi
- Generalises to: other nlink-jp browser/network tools

---

## Context

The MCP client (an LLM agent) driving chrome-pilot-mcp can be prompt-
injected. Browser automation then becomes, for an attacker:

- a way to reach arbitrary sites (malware, phishing pages)
- with `--attach`, a way to operate logged-in sessions
- an exfiltration channel (POSTing stolen data to attacker hosts)

We want a **server-side destination restriction** that does not depend on
the LLM's judgment. Configuration is fixed at startup (CLI flags or
config.toml — ADR-0002); no MCP tool can change it at run time, so the
LLM cannot loosen the limits through tool calls. This run-time
immutability is the core of the feature's security value.

Naming follows Chrome's own terminology shift: **allow/block**, not
white/black.

## Decision

### Flags and semantics

- `--allow-hosts "<glob>,<glob>,..."` — allowed hosts. **Specifying even
  one switches to default-deny** (everything off-list is refused). Unset
  means allow-all (backward compatible).
- `--block-hosts "<glob>,..."` — refused hosts. **Takes precedence over
  allow.** Block-only usage (denylist mode) is valid.
- `--block-local` — independently refuses navigation to `file://` and
  `data:`. Default is allowed (local HTML verification is a primary use).

Host matching rules:

- Case-insensitive exact match, or glob (`*` matches one or more labels).
- `*.example.com` matches **subdomains only**, not `example.com` itself
  (security configs favor explicitness). List both to include the apex.
- Ports are not matched (hostname only). Applies to http/https.

### Two-layer enforcement

1. **Tool-argument layer** — `navigate_page` / `new_page` URLs are checked
   up front; refusals return the structured error `host_not_allowed`.
   This is the UX layer that tells the agent clearly "you can't go there".
2. **CDP network layer** — when any list is configured, each page session
   gets `Fetch.enable` on attach; `Fetch.requestPaused` checks the request
   host and answers `continueRequest` / `failRequest (BlockedByClient)`.
   In-page JS redirects, fetch/XHR, subresources, and click-driven
   navigations are stopped here.

With no lists configured, `Fetch.enable` is never called (zero
interception overhead). Blocked requests surface as `loadingFailed
(BlockedByClient)` in the network collector and are observable via
`list_network_requests` (debuggability).

### Implementation placement

- The decision logic is a pure function `hostAllowed(host) bool` in
  `internal/tools`, table-tested.
- `Fetch.requestPaused` arrives on the collector path (CDP read loop);
  since answering requires CDP calls, responses go through a goroutine,
  same as the screencast frame ack.

## Alternatives considered

- **`Network.setBlockedURLs` alone**: minimal implementation but can only
  express denylists, not default-deny. Rejected.
- **Chrome `--host-rules` / `--host-resolver-rules`**: maps hosts to
  127.0.0.1 to effectively block. Poor allowlist expression, opaque
  errors. Rejected.
- **In-process CONNECT proxy (`--proxy-server=127.0.0.1:<port>`)**: an
  stdlib CONNECT proxy inside the binary controlling all of Chrome's
  egress at the process level — the **strongest boundary** (covers
  WebSockets and un-attached pages, no TLS interception needed; the
  CONNECT target host is enough). Launch mode only (a proxy cannot be
  injected into an already-running attached browser). Not adopted now,
  but **kept as the hardening path** if the residual risks below matter.

## Consequences

- Default (no lists) is fully compatible in behavior and overhead.
- With lists set, every request pays one CDP interception round-trip
  (loopback; negligible in practice).
- **Known limits (v1)**:
  - WebSocket connections are outside the `Fetch` domain → in allow mode,
    WS to off-list hosts is not blocked.
  - Pages the tool never attached (e.g. tabs a user opens by hand in a
    headful launch) are not covered.
  - `file://` / `data:` enforcement is tool-argument-layer only (`Fetch`
    covers network requests).
  - If these gaps become material, migrate to the CONNECT proxy design.
- Documentation states plainly: this is an agent-mishap boundary, not a
  substitute for OS-level egress control.
