# ADR-0003: Browser profile selection and isolation

- Status: Proposed
- Date: 2026-07-29
- Driver: magi
- Depends on: ADR-0002 (config.toml schema)

---

## Context

Launch mode currently starts Chrome with a fresh temporary user-data-dir
and deletes it on shutdown (full isolation). A safe default, but it
cannot serve:

- **Persistent login state**: sign in to a service under test once and
  reuse cookies across sessions (attaching to the real browser every time
  is overkill and weakens the ADR-0001 boundary)
- **Purpose separation**: stateful profiles ("work verification",
  "experiments") that never mix

Persistent profiles also raise the stakes: they accumulate authenticated
state, so confusion with the user's **real Chrome profile** must be
impossible.

## Decision

### Three modes

| Mode | Selection | Behavior |
|---|---|---|
| ephemeral (default) | nothing | temp dir, deleted on shutdown (as today) |
| named profile | `--profile <name>` | persistent profile under the tool's managed directory; created if absent, **never deleted** on shutdown |
| explicit path | `--user-data-dir <path>` | use the path as-is; created if absent, never deleted |

- Named profiles live at
  `os.UserConfigDir()/chrome-pilot-mcp/profiles/<name>` (macOS:
  `~/Library/Application Support/chrome-pilot-mcp/profiles/<name>`),
  created with mode 0700.
- `<name>` is restricted to `[a-zA-Z0-9_-]+` (syntactically excludes path
  separators and `..`).
- config.toml (ADR-0002) gains `profile` / `user_data_dir` keys under
  `[browser]`; precedence stays flags > config.

### Guardrails

1. `--profile` together with `--user-data-dir` is an **error**.
2. Either together with `--attach` is an error (an attached browser's
   profile cannot be chosen).
3. `--user-data-dir` pointing into the **real Chrome profile area**
   (macOS `~/Library/Application Support/Google/Chrome*`, Linux
   `~/.config/google-chrome*` / `~/.config/chromium*`, Windows
   `%LOCALAPPDATA%\Google\Chrome\User Data`) is **refused at startup** —
   prevents damaging real browser state and SingletonLock collisions.
   The legitimate "use my real profile" case is served by `--attach`.
4. Launching the same profile twice fails via Chrome's ProcessSingleton;
   the launcher detects this and converts it into a clear
   `browser_launch_failed`: "profile is in use by another Chrome
   instance" (instead of the raw "exited before printing endpoint").

### Security framing

The recommended pairing is a named profile plus an ADR-0001 allow-list:
an automation environment that carries authenticated state but whose
destinations are closed by a whitelist. Documentation states that
persistent profiles accumulate credentials and must not be backed up or
shared.

## Alternatives considered

- **Allow driving the real Chrome profile directly**: SingletonLock
  collisions, profile-version incompatibilities, extension side effects.
  Attach mode already serves this safely. Rejected.
- **Clone-the-real-profile launches**: huge (GBs), slow, and duplicating
  authenticated state is poor security practice. Rejected.
- **Profiles under the workspace**: the workspace is an output area
  defaulting to a temp dir — wrong home for persistent data. Rejected.

## Consequences

- Default behavior (ephemeral) is unchanged — isolation stays standard.
- Profile listing/deletion is out of scope (the directory is
  self-explanatory; a `profiles` subcommand can be considered later).
- Persistent profiles are migrated forward by Chrome across upgrades;
  downgrade incompatibility is noted in the docs.
