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
// the credential and agent-control blacklist, and this server's own
// directory. A work_dir may legitimately be a parent of either — ~/.config,
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
func outputUnder(wsRoot, filePath string) (string, error) {
	raw := filepath.Clean(filePath)
	if !filepath.IsAbs(raw) {
		raw = filepath.Join(wsRoot, raw)
	}
	real := resolveExisting(raw)
	rel, err := filepath.Rel(wsRoot, real)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", toolerr.Newf(toolerr.CodePathNotAllowed,
			"filePath %q is outside work_dir %q: files are written only under the work directory the call names — "+
				"pass a path relative to it, or leave filePath out", filePath, wsRoot).
			WithDetails(map[string]any{"reason": "outside_work_dir", "filePath": filePath, "work_dir": wsRoot})
	}
	if reason, why := refusedLocation(raw, real); reason != "" {
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
func inputUnder(wsRoot, filePath string) (string, error) {
	raw := filePath
	if !filepath.IsAbs(raw) {
		raw = filepath.Join(wsRoot, raw)
	}
	raw = filepath.Clean(raw)
	real, err := filepath.EvalSymlinks(raw)
	if err != nil {
		return "", toolerr.Newf(toolerr.CodeInvalidArguments, "filePath: %v", err)
	}
	if reason, why := refusedLocation(raw, real); reason != "" {
		return "", toolerr.Newf(toolerr.CodePathNotAllowed, "filePath %q is refused: %s", filePath, why).
			WithDetails(map[string]any{"reason": reason, "filePath": filePath})
	}
	rel, err := filepath.Rel(wsRoot, real)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", toolerr.Newf(toolerr.CodePathNotAllowed,
			"filePath %q is outside work_dir %q: a page can send what it is given anywhere, so only a file under "+
				"the work directory is handed over — copy it there first", filePath, wsRoot).
			WithDetails(map[string]any{"reason": "outside_work_dir", "filePath": filePath, "work_dir": wsRoot})
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

// refusedLocation reports why a file under work_dir is still refused — its
// details.reason and a sentence — or two empty strings. paths are the forms
// of one path (as given and resolved), each compared with each form of every
// entry, for the reason workdir.Sensitive documents.
func refusedLocation(paths ...string) (reason, why string) {
	if why := workdir.Sensitive(paths...); why != "" {
		return "sensitive_path", why
	}
	for _, d := range serverOwnedDirs() {
		own, err := os.Stat(d)
		if err != nil {
			continue // not there: nothing of this server's to reach
		}
		for _, p := range paths {
			if insideByIdentity(p, own) {
				return "server_dir", "it is inside this server's own directory " + d +
					" (config.toml and the managed browser profiles)"
			}
		}
	}
	return "", ""
}

// insideByIdentity reports whether p, or an existing directory above it, is
// the directory dir describes — compared by file identity, not by name. Names
// mislead here: APFS is case-insensitive by default, so CHROME-PILOT-MCP and
// chrome-pilot-mcp are one directory under two spellings, and a string
// comparison passes the spelling it was not written for (the second review of
// ADR-0006 got a profile's Cookies uploaded that way).
func insideByIdentity(p string, dir os.FileInfo) bool {
	for cur := p; ; {
		if fi, err := os.Stat(cur); err == nil && os.SameFile(fi, dir) {
			return true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return false
		}
		cur = parent
	}
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
func writeUnder(root *os.Root, rel string, write func(io.Writer) error) error {
	dir := filepath.Dir(rel)
	if dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create output dir: %w", err)
		}
	}
	if real, err := filepath.EvalSymlinks(filepath.Join(root.Name(), dir)); err == nil {
		if reason, why := refusedLocation(real); reason != "" {
			return toolerr.Newf(toolerr.CodePathNotAllowed, "%s is refused: %s", rel, why).
				WithDetails(map[string]any{"reason": reason, "path": rel})
		}
	}
	tmp, f, err := createExclusive(root, dir, filepath.Base(rel))
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

// createExclusive creates a new file beside name under a random temporary
// name, failing rather than opening anything already there.
func createExclusive(root *os.Root, dir, name string) (string, *os.File, error) {
	for range 4 {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", nil, err
		}
		tmp := filepath.Join(dir, "."+name+"."+hex.EncodeToString(b[:])+".partial")
		f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return tmp, f, err
	}
	return "", nil, fmt.Errorf("no free temporary name beside %s", name)
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
// appended unchanged. p must be absolute and clean. A chain of links that
// does not end is returned as it stands; the write's os.Root refuses it.
func resolveExisting(p string) string {
	for hops := 0; hops < 40; hops++ {
		cur, tail := p, ""
		for {
			if r, err := filepath.EvalSymlinks(cur); err == nil {
				return filepath.Join(r, tail)
			}
			if fi, err := os.Lstat(cur); err == nil && fi.Mode()&os.ModeSymlink != 0 {
				target, err := os.Readlink(cur)
				if err != nil {
					return p
				}
				if !filepath.IsAbs(target) {
					target = filepath.Join(filepath.Dir(cur), target)
				}
				p = filepath.Join(filepath.Clean(target), tail)
				break // resolve again from the link's target
			}
			parent := filepath.Dir(cur)
			if parent == cur {
				return p
			}
			tail = filepath.Join(filepath.Base(cur), tail)
			cur = parent
		}
	}
	return p
}
