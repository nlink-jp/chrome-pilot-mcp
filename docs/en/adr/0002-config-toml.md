# ADR-0002: config.toml support (in-house TOML subset loader)

- Status: Proposed
- Date: 2026-07-29
- Driver: magi
- Supersedes: RFP §2 Configuration "CLI flags only"
- Generalises to: config-file support in other zero-dependency tools

---

## Context

Launch options have grown (6 browser flags + workspace + 3 security flags
from ADR-0001); listing them all in the MCP client's `args` array is hard
to read and reuse. We want the org-standard sectioned TOML config.

Two project-specific constraints apply:

1. **Zero-dependency policy** — the org's Go loader pattern is
   `BurntSushi/toml`, but no external modules are allowed (CLAUDE.md
   rule 1).
2. **The config itself is attack surface** — `executable_path` leads to
   arbitrary binary execution and `allow_hosts` loosens the ADR-0001
   boundary. Auto-reading a config from the cwd (data-toolbox-mcp also
   searches `./config.toml`) would mean that the common pattern "launch
   the MCP server with a cloned repository as cwd" **reads an
   attacker-authored file in that repository as configuration**.

## Decision

### Loading and precedence

- `--config <path>` — explicit path; a missing file is a **startup
  error**.
- Without it, only `~/.config/chrome-pilot-mcp/config.toml` is searched
  (absent → built-in defaults). **`./config.toml` is never read** — a
  deliberate deviation from org practice to avoid the config-injection
  risk above.
- Precedence: **CLI flags > config.toml > built-in defaults**. Explicitly
  set flags are detected via `flag.Visit`; only those override config.
- No env-var layer (no secrets are handled; the credential concerns that
  motivated the Vertex-tool unification do not exist here). If needed
  later: `CHROME_PILOT_<FIELD>`.

### Schema (sectioned TOML)

```toml
[browser]
headless        = false
channel         = "stable"     # stable | beta | dev | canary
executable_path = ""
attach          = ""           # ws://... | port | host:port (loopback only)
viewport        = "1280x800"
profile         = ""           # named persistent profile (ADR-0003)
user_data_dir   = ""           # explicit path (ADR-0003; exclusive with profile)

[workspace]
root = ""                      # empty = temp dir

[security]                     # ADR-0001
allow_hosts = []               # e.g. ["example.com", "*.example.com"]
block_hosts = []
block_local = false
```

Keys map 1:1 to CLI flags (`--executable-path` ↔ `executable_path`).

### In-house TOML subset loader

Implemented in `internal/config`. Supported syntax is the minimum a
config needs:

- `[section]`, `key = value`, `#` comments
- Values: basic strings `"..."` (escapes `\\` `\"` `\n` `\t`), integers,
  booleans, single-line string arrays `["a", "b"]`
- **Unsupported syntax fails loudly** (multiline strings, dotted keys,
  inline tables, dates, floats → "unsupported TOML syntax at line N")
- Unknown keys/sections are errors too (same strict-decode stance as the
  rest of the org tooling)

This is not a TOML reimplementation; it is defined as "the TOML subset in
which a chrome-pilot-mcp config can be written", with the supported
syntax documented.

## Alternatives considered

- **Add BurntSushi/toml**: best for loader unification, but the
  zero-dependency stance is the project's reason to exist. Rejected.
- **JSON config**: stdlib-complete but off-convention (org configs are
  sectioned TOML) and comment-free. Rejected.
- **Also search `./config.toml`**: org practice, but the Context-2 risk
  is real for this tool. Rejected (explicit `--config` covers the need).

## Consequences

- MCP client config shrinks to `"args": ["--config", "/path"]` or just a
  file at `~/.config/chrome-pilot-mcp/config.toml`.
- Flag-only operation stays fully supported (no config required).
- We own the maintenance of the TOML subset (pinned by table tests).
  Requests for out-of-subset syntax get evaluated case by case.
- RFP §2's "CLI flags only, no config file" is updated by this ADR.
