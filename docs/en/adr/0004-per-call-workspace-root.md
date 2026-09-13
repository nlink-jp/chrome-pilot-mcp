# ADR-0004: Per-call workspace root for file-producing tools

- Status: Accepted
- Date: 2026-09-13
- Driver: magi
- Depends on: ADR-0002 (config.toml schema)

---

## Context

Two tools produce a file: `take_screenshot` writes a PNG/JPEG, and
`screencast_stop` writes the GIF that `screencast_start` began recording.
Both land under a workspace root chosen once, at startup — `--workspace-root`
or `[workspace] root`, defaulting to a fresh temp directory — and the tool
result hands back the path.

The caller is normally an agent runtime, and the ones this server is
registered with confine their own file access:

- **gem-agent / lagent** resolve every built-in file tool against exactly two
  roots — the project directory and the session work directory — and refuse
  anything else, symlinks included. A path under `/var/folders/...` is
  refused by `view_image`, so a screenshot the agent just took is one it
  cannot look at.
- **Claude Code** is confined to the workspace directory in the same way.

The startup flag cannot close the gap on its own. The value that would work
is per-session (`$GEMAGENT_WORK_DIR`, `$LAGENT_WORK_DIR`), so it has to be
written into each runtime's own server registration, and a runtime with no
variable to expand — or a session with no work directory — is back where it
started. Meanwhile the information needed to pick the directory is held by
the caller, not by whoever wrote the launch line.

`screencast_start` already accepted an arbitrary absolute `filePath` and
created its parent directory, so "the call names where the file goes" is not
a new capability for this server; screenshots simply had no equivalent.

## Decision

`take_screenshot` and `screencast_start` accept an optional `workspaceRoot`.

- When given, the file is written under `<workspaceRoot>/screenshots` or
  `<workspaceRoot>/screencasts`; the directory is created if missing.
- When omitted, behaviour is exactly as before: the configured root, else a
  fresh temp directory.
- An explicit `filePath` on `screencast_start` still wins over both — it
  names the file, not a root.
- `workspaceRoot` must be **absolute**. A tool argument is JSON: nothing
  expands `~` and nothing resolves a relative path on the way in, so either
  would silently land the file next to the server's working directory.
  Both are refused as `invalid_arguments`.
- `screencast_start` validates the root even though the file is written at
  stop time. The call that supplied the bad argument is the one that can fix
  it, and a recording that only fails at the end has already discarded its
  frames.
- The argument is spelled `workspaceRoot`, matching this server's camelCase
  schema style (`filePath`, `fullPage`). The sibling MCP servers in the org
  spell the same concept `workspace_root` in their snake_case schemas; a call
  that guesses wrong is rejected by name (`DisallowUnknownFields`), which is
  a correction the caller can act on rather than a silent no-op.

## Consequences

- An agent gets back a path it can open, without the operator having to
  thread a per-session variable through the server's launch line.
- The server writes wherever the caller says. This is a deliberate
  non-boundary: the tool argument is the same trust level as `filePath`,
  which has always been unrestricted, and the containment that matters for
  an agent-driven write lives in the agent's own sandbox. Do **not** read
  this as a sandbox — chrome-pilot-mcp has no filesystem policy, and adding
  one belongs in its own ADR.
- Startup configuration stays meaningful as the default for callers that
  pass nothing (a human driving the server by hand, an MCP client with no
  directory of its own).
