# ADR-0007: Leave path judgement to nlink-jp/pathguard — drop the identity layer of our own

- Status: Accepted
- Date: 2026-09-22
- Amends: [ADR-0006](0006-writes-and-uploads-under-work-dir.md) (the judgement of the refused places)

## Context

ADR-0005 added `work_dir` validation and ADR-0006 the confinement of writes and uploads. The judgement
of the refused places had two layers: the name comparison the organization shared (`workdir.Sensitive`,
a copy in `internal/workdir`) and an identity comparison this server added of its own
(`refusedLocation`, `insideByIdentity`, comparing the protected directories with `os.SameFile`),
because APFS is case-insensitive by default and two reviews in a row broke the name comparison.

The organization moved this judgement into one module (`nlink-jp/pathguard`, lib-series). It compares
places by file identity and by names folded the way the disk folds them, catches a place that does
not exist yet through the identity of its parent, holds the same list as gem-agent and lagent, and
separates reads and writes (Local) from what leaves the machine (Outbound).

## Decision

- Depend on `github.com/nlink-jp/pathguard` v0.2.0. CLAUDE.md's dependency rule is reworded to "no
  third-party modules; modules of this organization, which hold the same rule, are allowed" (the
  operator's decision, 2026-09-22).
- `internal/workdir` becomes a thin adapter: taking `_meta`, carrying errors onto `toolerr`,
  `NewResolverFor(places...)` (naming its own places), and `LocalPath` and `OutboundPath` (returning a
  details.reason and a sentence).
- The protected places are one list, `Manager.protectedPlaces` — this server's own directory, the
  profile of the Chrome it drives, every `chrome-pilot-mcp-profile-*` in the temp directory, the
  user's own Chrome profiles — each with its reason (`server_dir`, `browser_profile`) and sentence.
  `Manager.resolver` builds a resolver from it at the moment of use: the places change while the
  server runs (a browser is launched, a throwaway profile appears), so nothing is cached.
- `work_dir` is judged by pathguard/workdir's list of what may not be a work directory (system
  directories, the home directory itself, the credential and agent-control places, and this server's
  places; `Resolve`); writes (`outputUnder`, `writeUnder`) by the Local policy (`LocalPath`);
  `upload_file` (`inputUnder`) by the Outbound policy (`OutboundPath`). A page can send what it is
  given anywhere, so an upload is treated as leaving the machine.
- `refusedLocation`, `insideByIdentity` and `protectedDir` are dropped. `resolveExisting` (where a
  file would be created; a dangling link whose target climbs with `..` is not trusted) and the writes
  through an `os.Root` with an unguessable temporary name and a rename stay: they are not judgement
  but the confinement of writing what was judged.

## Consequences

- **Refused now on upload**: a file named as a secret (`id_rsa`, `credentials.json`,
  `*service-account*.json`, `.env`) or whose path passes through a credential directory or file name
  (`.ssh`, `.aws`, `.npmrc`, `.netrc`, `.git-credentials`, `.bash_history` and the like) — wherever it
  sits, inside `work_dir` and outside your home included.
- **Refused now for writes and `work_dir`**: the real places under your home from the runtimes'
  list, under every spelling, and wherever a link directly inside one of those directories points;
  when `$HOME` names another directory than the account's home, those places under both (this
  server's own directory follows `$HOME`, as it did before; your own Chrome profiles are protected
  under both homes, a relative `$HOME` never counting as one); everything when the home is unknown.
- **Accepted now**: the `.env` templates (`.env.example` and the like).
- `work_dir_denied` carries `reason` in its `details`.
- The after-creation check in `writeUnder` (the directory the root actually reached) guards a race no
  deterministic test can provoke, and a mutation of it fails no test; the same as in v0.8.0.

## References

- Organization ADR-021 (the work-dir contract of the file-mediated MCP servers)
- ADR-0005 (work-dir contract), ADR-0006 (writes and uploads stay under `work_dir`)
- nlink-jp/pathguard's RFP (`docs/en/pathguard-rfp.md`)
