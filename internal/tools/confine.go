package tools

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/nlink-jp/chrome-pilot-mcp/internal/toolerr"
	"github.com/nlink-jp/chrome-pilot-mcp/internal/workdir"
)

// This file keeps a caller-named path inside the caller's work directory
// (organization ADR-021 §7, project ADR-0006). Two rules, one per direction:
//
//   - A file this server writes lands only under work_dir. A write anywhere
//     else is a persistence channel (a shell profile, a git hook, a launchd
//     plist) opened on a model's say-so, and no workflow needs it.
//   - A file this server hands to a web page comes only from under work_dir.
//     The page can send it anywhere, so "the caller could have read it
//     itself" does not hold, and the credential blacklist alone is a floor:
//     the next secret file is not on it.
//
// Inside work_dir two kinds of place are still refused, in both directions:
// the credential and agent-control places, and this server's own directories
// (its config, the profile of the browser it drives, throwaway profiles, the
// user's own Chrome profiles). Both are nlink-jp/pathguard's judgement, by
// file identity and by folded name (ADR-0007): a work_dir under its list
// of what may not be a work directory, writes under its Local policy, an
// upload — which leaves the machine — under its Outbound policy. A work_dir may legitimately be a parent of either — ~/.config,
// ~/Library/Application Support — and the files under it are then the
// managed browser profiles' cookies, or config.toml.

// outputUnder returns where a caller-named output file goes, as a path
// relative to wsRoot, the resolved work directory. A relative filePath is
// relative to work_dir; an absolute one must lie inside it, in either
// spelling of the directory — the caller may name it as it gave it, before
// its symlinks were resolved.
//
// The check follows every symlink along the path, dangling ones included, so
// a link inside work_dir that points out of it is refused here, at the call
// that named it. The write itself then goes through an os.Root opened on
// work_dir (writeUnder), which refuses such a link again if one appears in
// between.
func outputUnder(wsRoot, filePath string, r workdir.Resolver) (string, error) {
	raw := filepath.Clean(filePath)
	if !filepath.IsAbs(raw) {
		raw = filepath.Join(wsRoot, raw)
	}
	real, ok := resolveExisting(raw)
	rel, err := filepath.Rel(wsRoot, real)
	if !ok || err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", toolerr.Newf(toolerr.CodePathNotAllowed,
			"filePath %q is outside work_dir %q: files are written only under the work directory the call names — "+
				"pass a path relative to it, or leave filePath out", filePath, wsRoot).
			WithDetails(map[string]any{"reason": "outside_work_dir", "filePath": filePath, "work_dir": wsRoot})
	}
	if reason, why := r.LocalPath(raw, real); why != "" {
		return "", toolerr.Newf(toolerr.CodePathNotAllowed, "filePath %q is refused: %s", filePath, why).
			WithDetails(map[string]any{"reason": reason, "filePath": filePath})
	}
	return rel, nil
}

// inputUnder resolves the file upload_file hands to a page and returns its
// real path. The file must exist, be a regular file, stay off the refused
// locations, and lie under wsRoot. The refused locations are checked first,
// on both spellings of the path, so a link from work_dir into ~/.ssh is
// refused as a credential file and not merely as an outside one.
//
// What this cannot close: Chrome is given a path and opens the file later, so
// a file swapped for a link after this check is read as the link's target.
// DOM.setFileInputFiles takes paths, not open files.
func inputUnder(wsRoot, filePath string, r workdir.Resolver) (string, error) {
	raw := filePath
	if !filepath.IsAbs(raw) {
		raw = filepath.Join(wsRoot, raw)
	}
	raw = filepath.Clean(raw)
	// Judged before anything resolves it: pathguard follows the links itself
	// and needs no existing file, and "no such file" versus "refused" would
	// otherwise say whether a credential file exists.
	if reason, why := r.OutboundPath(raw, raw); why != "" {
		return "", toolerr.Newf(toolerr.CodePathNotAllowed, "filePath %q is refused: %s", filePath, why).
			WithDetails(map[string]any{"reason": reason, "filePath": filePath})
	}
	outside := func() error {
		return toolerr.Newf(toolerr.CodePathNotAllowed,
			"filePath %q is outside work_dir %q: a page can send what it is given anywhere, so only a file under "+
				"the work directory is handed over — copy it there first", filePath, wsRoot).
			WithDetails(map[string]any{"reason": "outside_work_dir", "filePath": filePath, "work_dir": wsRoot})
	}
	// Where the path lands, a missing tail included, before anything says
	// whether it exists: outside work_dir, an existing file and a missing one
	// get the same answer.
	where, ok := resolveExisting(raw)
	if !ok {
		return "", toolerr.Newf(toolerr.CodePathNotAllowed,
			"filePath %q is refused: a symbolic link on the path cannot be followed safely", filePath).
			WithDetails(map[string]any{"reason": "outside_work_dir", "filePath": filePath, "work_dir": wsRoot})
	}
	if !under(wsRoot, where) {
		return "", outside()
	}
	real, err := filepath.EvalSymlinks(raw)
	if err != nil {
		return "", toolerr.Newf(toolerr.CodeInvalidArguments, "filePath: %v", err)
	}
	if reason, why := r.OutboundPath(raw, real); why != "" {
		return "", toolerr.Newf(toolerr.CodePathNotAllowed, "filePath %q is refused: %s", filePath, why).
			WithDetails(map[string]any{"reason": reason, "filePath": filePath})
	}
	if !under(wsRoot, real) {
		return "", outside()
	}
	fi, err := os.Stat(real)
	if err != nil {
		return "", toolerr.Newf(toolerr.CodeInvalidArguments, "filePath: %v", err)
	}
	if !fi.Mode().IsRegular() {
		return "", toolerr.Newf(toolerr.CodeInvalidArguments, "filePath %q is not a regular file", filePath)
	}
	return real, nil
}

// under reports whether p lies strictly inside dir.
func under(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != "." && filepath.IsLocal(rel)
}

// writeUnder writes a file at rel under root, creating its directory.
//
//   - root refuses any path, and any symlink along it, that leaves the
//     directory it was opened on.
//   - The directory the file lands in is checked against the refused
//     locations by identity once it exists, so a screenshots/ or screencasts/
//     that is a link into this server's own directory, still inside work_dir,
//     is refused too — every write, not only a caller-named one.
//   - The file is written under a temporary name nobody can guess, created
//     exclusively, and renamed into place: an entry already at rel — a hard
//     link to a file outside work_dir, or a symlink — is replaced rather than
//     written through, and so is nothing planted at the temporary name.
//
// A refusal comes back as a *toolerr.Error; anything else is an I/O failure
// for the caller to report (writeFailure).
func writeUnder(root *os.Root, rel string, r workdir.Resolver, write func(io.Writer) error) error {
	dir := filepath.Dir(rel)
	refuse := func(reason, why string) error {
		return toolerr.Newf(toolerr.CodePathNotAllowed, "%s is refused: %s", rel, why).
			WithDetails(map[string]any{"reason": reason, "path": rel})
	}
	// Before anything is created: where the directory would be, links and all,
	// so not even an empty directory appears in a protected place.
	where, ok := resolveExisting(filepath.Join(root.Name(), dir))
	if !ok {
		return refuse("outside_work_dir", "a symbolic link on the output path cannot be followed safely")
	}
	// The path as named goes too: pathguard follows every hop of a chain of
	// links from it, and the resolved end alone has lost them.
	if reason, why := r.LocalPath(filepath.Join(root.Name(), dir), where); why != "" {
		return refuse(reason, why)
	}
	if dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create output dir: %w", err)
		}
	}
	// And after: the directory the root actually reaches must be the one the
	// path names, and that one must not be protected. A work_dir moved while
	// a recording was running fails the first test; nothing unresolvable
	// passes.
	real, err := filepath.EvalSymlinks(filepath.Join(root.Name(), dir))
	if err != nil {
		return refuse("outside_work_dir", "the output directory cannot be resolved: "+err.Error())
	}
	viaRoot, rerr := root.Stat(dir)
	viaPath, perr := os.Stat(real)
	if rerr != nil || perr != nil || !os.SameFile(viaRoot, viaPath) {
		return refuse("outside_work_dir", "the work directory is no longer where it was when the call began")
	}
	if reason, why := r.LocalPath(filepath.Join(root.Name(), dir), real); why != "" {
		return refuse(reason, why)
	}
	tmp, f, err := createExclusive(root, dir)
	if err != nil {
		return fmt.Errorf("create %s: %w", rel, err)
	}
	werr := write(f)
	cerr := f.Close()
	if err := firstErr(werr, cerr); err != nil {
		_ = root.Remove(tmp)
		return fmt.Errorf("write %s: %w", rel, err)
	}
	if err := root.Rename(tmp, rel); err != nil {
		_ = root.Remove(tmp)
		return fmt.Errorf("place %s: %w", rel, err)
	}
	return nil
}

// createExclusive creates a new file in dir under a random temporary name,
// failing rather than opening anything already there. The name does not grow
// with the final one, so a long file name still has room.
func createExclusive(root *os.Root, dir string) (string, *os.File, error) {
	for range 4 {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", nil, err
		}
		tmp := filepath.Join(dir, ".cp-"+hex.EncodeToString(b[:])+".partial")
		f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return tmp, f, err
	}
	return "", nil, fmt.Errorf("no free temporary name in %s", dir)
}

// writeFailure turns a writeUnder error into the tool's error: a refusal as it
// is, anything else as workspace_failed.
func writeFailure(err error, what string) error {
	var te *toolerr.Error
	if errors.As(err, &te) {
		return te
	}
	return toolerr.Newf(toolerr.CodeWorkspaceFailed, "%s: %v", what, err)
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// resolveExisting returns the real location a file created at p would have:
// the symlinks of every existing component resolved — a dangling one by its
// target, since creating through it would create the target — and the rest
// appended unchanged. p must be absolute and clean.
//
// ok is false when the answer cannot be trusted, and the caller refuses: a
// dangling link whose target climbs with "..", because joining it cancels a
// component by its name before that component's own link is resolved (the
// last review walked a pre-check into a protected directory that way), a
// link that cannot be read, or a chain of links that does not end.
func resolveExisting(p string) (real string, ok bool) {
	for hops := 0; hops < 40; hops++ {
		cur, tail := p, ""
		for {
			if r, err := filepath.EvalSymlinks(cur); err == nil {
				return filepath.Join(r, tail), true
			}
			if fi, err := os.Lstat(cur); err == nil && fi.Mode()&os.ModeSymlink != 0 {
				target, err := os.Readlink(cur)
				if err != nil || hasParentSegment(target) {
					return "", false
				}
				if !filepath.IsAbs(target) {
					parent, err := filepath.EvalSymlinks(filepath.Dir(cur))
					if err != nil {
						return "", false
					}
					target = filepath.Join(parent, target)
				}
				p = filepath.Join(filepath.Clean(target), tail)
				break // resolve again from the link's target
			}
			parent := filepath.Dir(cur)
			if parent == cur {
				return p, true
			}
			tail = filepath.Join(filepath.Base(cur), tail)
			cur = parent
		}
	}
	return "", false
}

// hasParentSegment reports whether a path has a ".." component.
func hasParentSegment(p string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(p), "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}
