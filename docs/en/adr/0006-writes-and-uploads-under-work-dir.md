# ADR-0006: The screencast file and the uploaded file both stay under `work_dir`

- Status: Accepted — the judgement of the refused places is replaced by [ADR-0007](0007-pathguard.md)
  (nlink-jp/pathguard)
- Date: 2026-09-22
- Amends: [ADR-0005](0005-work-dir-contract.md)

## Context

ADR-0005 made `work_dir` required on every tool that writes a file, and the
organization's contract (ADR-021) lists this server as done. Two file arguments
had been left out of it:

1. **`screencast_start`'s `filePath`.** It was checked only for a `.gif` suffix.
   A caller could name any path the process could write. Missing parent
   directories were created, and an existing file was overwritten. ADR-021 §7
   says writes land only under `work_dir`: "No workflow in this fleet needs a
   server to write anywhere else". A write elsewhere is the persistence channel
   (a shell profile, a git hook, a launchd plist) opened on a model's say-so.
2. **`upload_file`'s `filePath`.** It was checked only for existence, so a
   directory passed too, and the path went to Chrome unchanged. The credential
   blacklist that `internal/workdir` holds for exactly this purpose was never
   consulted. ADR-021 §7's exception covers a server that hands a file to
   something that can send it off the machine: "takes its input only from under
   `work_dir`". A file input on a web page is exactly that. The page decides
   where the file goes.

Tests pinned both behaviours (`TestScreencastFilePathBeatsWorkDir` wrote
outside `work_dir` on purpose). No document records them as a deliberate
exception. ADR-0004's "deliberate non-boundary" predates ADR-021, and ADR-0005
superseded it the same day.

## Decision

1. **`screencast_start`'s `filePath` names a place under `work_dir`.** A
   relative path is relative to it. An absolute path must lie inside it, in
   either spelling of the directory: as the caller gave it, or with its symlinks
   resolved. The check follows every symlink that already exists along the path,
   and it runs at `screencast_start`, the call that named the path (ADR-0004's
   discipline), before a frame is collected. The file is written through
   an `os.Root` opened on `work_dir` at start and held until stop, which refuses
   a symlink that would carry the write out of the directory if one appears in
   between — and, being opened at start, does not follow `work_dir` itself if it
   is swapped for a link meanwhile. A dangling link counts by its target, since
   creating through it would create the target.
2. **`upload_file` takes `work_dir`, required, and hands the page only a file
   under it.** `filePath` is relative to `work_dir` or absolute under it. The
   file's real path must lie under `work_dir`, stay off the credential blacklist,
   and be a regular file; it is that real path Chrome receives. The blacklist is
   checked first, on both spellings of the path (ADR-021 §7), so a link from
   `work_dir` into `~/.ssh` is refused as a credential file rather than merely as
   an outside one.
3. **Inside `work_dir`, some places stay refused in both directions**: the
   credential blacklist (`sensitive_path`), and the protected directories —
   this server's own directory (`config.toml` and the managed profiles), the
   profile of the Chrome it is driving, and every throwaway
   `chrome-pilot-mcp-profile-*` in the temp directory — each runtime runs its
   own server, and a killed one leaves its profile behind (`server_dir`) — and
   the user's own Chrome profiles (`browser_profile`). A `work_dir` may legitimately be a parent of any of
   them — the temp directory, `~/.config`, `~/Library/Application Support` —
   and the files under it would then be a live profile's cookies. A `work_dir`
   inside one is refused with `work_dir_denied`. The protected directories are
   compared by **file identity**, not by name: APFS is case-insensitive by
   default, and a string comparison let `CHROME-PILOT-MCP/profiles/…/Cookies`
   through (second review); the third found the throwaway profile unprotected.
4. **Every file this server writes goes through `writeUnder`**: an `os.Root`, so
   a link out of `work_dir` is refused; a check of where the directory would be,
   links followed, before anything is created, and again once it exists — that
   the root reaches the directory the path names, and that it is not protected —
   so a `screenshots/` or `screencasts/` linked into a protected place is refused
   without so much as an empty directory appearing there, and a `work_dir` moved
   during a recording is refused rather than written into; and a temporary name
   nobody can guess, of fixed length, created exclusively and renamed into
   place, so an existing entry (a hard link to a file outside `work_dir` among
   them) is replaced rather than written through, and nothing planted at a
   predictable temporary name is written through either. A dangling link whose
   target climbs with `..` is refused rather than followed: joining it would
   cancel a component by name before that component's own link is resolved.
5. **A refusal is `path_not_allowed`**, and `details.reason` says which rule:
   `outside_work_dir`, `sensitive_path`, `server_dir` or `browser_profile`. A
   missing file or a directory stays `invalid_arguments`.
6. `go.mod` moves to Go 1.25 for `os.Root.MkdirAll`. The standard library is
   still the only dependency.

## Consequences

- **Breaking.** An `upload_file` call without `work_dir` is refused with
  `work_dir_required`; a caller that uploaded from anywhere must first copy the
  file into its work directory. A `screencast_start` naming a path outside
  `work_dir` is refused at start. A runtime that sets `_meta["jp.nlink/work_dir"]`
  on every call needs no change to its `upload_file` calls.
- A relative `filePath` used to be relative to the server's own working
  directory; it is now relative to `work_dir`. An absolute `filePath` spelled
  through a symlinked directory comes back as its resolved path.
- `upload_file` now reports the file's real path in `uploaded`, which is the path
  Chrome was given; for a symlink the page sees the target's name.
- A `screencasts/` or `screenshots/` that is an **absolute** symlink, even one
  pointing inside `work_dir`, now fails with `workspace_failed`: `os.Root` refuses
  absolute links. A relative link inside `work_dir` works.
- `internal/workdir` is unchanged; the rules live in `internal/tools/confine.go`.
  Written files are now always mode `0644`.
- **What this does not close.** Chrome is handed a path and opens the file later,
  so a file swapped for a link after the check is read as the link's target;
  `DOM.setFileInputFiles` takes paths, not open files. An upload of a hard link
  to a file outside `work_dir` is not detected (ADR-021 does not ask for it). The
  blacklist comparison is case-sensitive, so on a case-insensitive filesystem
  `.ENV` or `~/.SSH` passes it; that is in `internal/workdir`, shared by the
  fleet, and is fixed there rather than here. A recording abandoned without
  `screencast_stop` keeps its frames and one open directory handle until the
  process exits. An attached browser's profile is not known to this server and
  is not protected beyond the user's own Chrome locations.

## References

- Organization ADR-021 §7; [ADR-0005](0005-work-dir-contract.md);
  [ADR-0004](0004-per-call-workspace-root.md)
- slack-mcp-extender's `internal/containment` — the first server under the
  upload exception, whose order of checks (resolve, blacklist, containment,
  regular file) this follows
