package tools

import (
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

// outputUnder returns where a caller-named output file goes, as a path
// relative to wsRoot, the resolved work directory. A relative filePath is
// relative to work_dir; an absolute one must lie inside it, in either
// spelling of the directory — the caller may name it as it gave it, before
// its symlinks were resolved.
//
// The check follows every symlink that already exists along the path, so a
// link inside work_dir that points out of it is refused here, at the call that
// named it. The write itself then goes through os.Root, which refuses such a
// link again if one appears in between.
func outputUnder(wsRoot, filePath string) (string, error) {
	p := filepath.Clean(filePath)
	if !filepath.IsAbs(p) {
		p = filepath.Join(wsRoot, p)
	}
	rel, err := filepath.Rel(wsRoot, resolveExisting(p))
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", toolerr.Newf(toolerr.CodePathNotAllowed,
			"filePath %q is outside work_dir %q: files are written only under the work directory the call names — "+
				"pass a path relative to it, or leave filePath out", filePath, wsRoot).
			WithDetails(map[string]any{"reason": "outside_work_dir", "filePath": filePath, "work_dir": wsRoot})
	}
	return rel, nil
}

// inputUnder resolves the file upload_file hands to a page and returns its
// real path. The file must exist, be a regular file, stay off the credential
// blacklist, and lie under wsRoot. The blacklist is checked first, on both
// spellings of the path, so a link from work_dir into ~/.ssh is refused as a
// credential file and not merely as an outside one.
func inputUnder(wsRoot, filePath string) (string, error) {
	raw := filePath
	if !filepath.IsAbs(raw) {
		raw = filepath.Join(wsRoot, raw)
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(raw))
	if err != nil {
		return "", toolerr.Newf(toolerr.CodeInvalidArguments, "filePath: %v", err)
	}
	if why := workdir.Sensitive(raw, real); why != "" {
		return "", toolerr.Newf(toolerr.CodePathNotAllowed, "filePath %q is refused: %s", filePath, why).
			WithDetails(map[string]any{"reason": "sensitive_path", "filePath": filePath})
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

// resolveExisting resolves the symlinks of the longest prefix of p that
// exists and appends the rest unchanged: the real location a file created at
// p would have. p must be absolute and clean.
func resolveExisting(p string) string {
	cur, tail := p, ""
	for {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, tail)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		tail = filepath.Join(filepath.Base(cur), tail)
		cur = parent
	}
}
