# ADR-0005: One output argument, `work_dir`; no default workspace and no launch flag

- Status: Accepted
- Date: 2026-09-13
- Amends: [ADR-0004](0004-per-call-workspace-root.md) (per-call workspaceRoot)
- Amended by: [ADR-0006](0006-writes-and-uploads-under-work-dir.md) (the screencast `filePath` and `upload_file` stay under `work_dir`)

## Context

ADR-0004 introduced `workspaceRoot` earlier today so that a file lands where the
caller can read it back. The judgement was right; two details of it disagree with
organization ADR-021, the fleet-wide work-directory contract.

1. **The spelling.** ADR-0004 chose `workspaceRoot` to match this server's
   camelCase schema style and accepted that the sibling servers spell the same
   concept `workspace_root`, with a wrong guess rejected by name. ADR-021 closes
   that split: **one name, `work_dir`, everywhere**.
2. **The default.** ADR-0004 kept "when omitted, behaviour is exactly as before"
   — the configured root, else a temp directory. ADR-021 §2 allows no
   server-owned default for a call that returns a path: whether the operator's
   directory is readable by the caller is coincidence, and when it is not, the
   old failure is back — the call succeeds and the path cannot be opened.

## Decision

1. The argument is **`work_dir`** (`take_screenshot`, `screencast_start`, the
   debug tools) and it is **required**.
2. Resolution is argument → `_meta["jp.nlink/work_dir"]` → error. **No default.**
3. The `--workspace-root` flag and the `[workspace] root` key are **deleted**; a
   config still carrying the key fails at startup with the reason named.
4. Validation is the closed list (absolute, no `~`, no `..`, exists and is a
   directory, writable, not a system or credential location). **The directory is
   not created** — the caller's own directory always exists, so a path that is
   not there is a typo.
5. ADR-0004's discipline of validating at `screencast_start` rather than at stop
   time stands.

## Consequences

- **Breaking.** A call sending `workspaceRoot` is refused, and a call written for
  upstream chrome-devtools-mcp (`take_screenshot` with no arguments) now fails
  with `work_dir_required`. Drop-in compatibility holds for tool names and
  behaviour; **the output destination is the one thing the caller must name**,
  and the error says so in one turn.
- The temp-directory fallback is gone, and with it the path where a screenshot is
  taken successfully and cannot be opened.
- `internal/workdir` is byte-identical to the copies in the rest of the fleet.

## References

- Organization ADR-021; [ADR-0004](0004-per-call-workspace-root.md); voice-scribe
  ADR-0010 (the reference implementation)
