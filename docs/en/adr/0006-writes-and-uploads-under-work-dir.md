# ADR-0006: The screencast file and the uploaded file both stay under `work_dir`

- Status: Accepted
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
   `os.Root` on `work_dir`, which refuses a symlink that would carry the write out
   of the directory if one appears between start and stop.
2. **`upload_file` takes `work_dir`, required, and hands the page only a file
   under it.** `filePath` is relative to `work_dir` or absolute under it. The
   file's real path must lie under `work_dir`, stay off the credential blacklist,
   and be a regular file; it is that real path Chrome receives. The blacklist is
   checked first, on both spellings of the path (ADR-021 §7), so a link from
   `work_dir` into `~/.ssh` is refused as a credential file rather than merely as
   an outside one.
3. **A refusal is `path_not_allowed`**, and `details.reason` says which rule:
   `outside_work_dir` or `sensitive_path`. A missing file or a directory stays
   `invalid_arguments`.
4. `go.mod` moves to Go 1.25 for `os.Root.MkdirAll`. The standard library is
   still the only dependency.

## Consequences

- **Breaking.** An `upload_file` call without `work_dir` is refused with
  `work_dir_required`; a caller that uploaded from anywhere must first copy the
  file into its work directory. A `screencast_start` naming a path outside
  `work_dir` is refused at start. A runtime that sets `_meta["jp.nlink/work_dir"]`
  on every call needs no change to its `upload_file` calls.
- `upload_file` now reports the file's real path in `uploaded`, which is the path
  Chrome was given.
- `internal/workdir` is unchanged and stays byte-identical to the fleet's copies;
  the rules live in `internal/tools/confine.go`.

## References

- Organization ADR-021 §7; [ADR-0005](0005-work-dir-contract.md);
  [ADR-0004](0004-per-call-workspace-root.md)
- slack-mcp-extender's `internal/containment` — the first server under the
  upload exception, whose order of checks (resolve, blacklist, containment,
  regular file) this follows
