package tools

import (
	"context"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/nlink-jp/pathguard"

	"github.com/nlink-jp/chrome-pilot-mcp/internal/toolerr"
	"github.com/nlink-jp/chrome-pilot-mcp/internal/workdir"
)

// Local files (ADR-0008). A file:// URL opens only when it lies inside the
// work directory the call names and passes pathguard's Local policy; that
// work directory is then granted to the page's session. Three places hold
// the rule, and all three judge with judgeLocal:
//
//   - the tool arguments (checkLocalURL, for navigate_page and new_page),
//   - the Fetch interception (fileRequestAllowed), which sees every file://
//     load of an attached session — JavaScript navigation, subresources,
//     iframes, view-source: — and fails what no grant covers,
//   - selectedPage (refuseUngrantedLocal), which keeps every tool but
//     navigate_page off a page showing a local file no grant covers: a tab a
//     script opened with window.open loads before it is attached, so the
//     interception never saw it.
//
// None of them takes m.mu: the interception decides while a tool call may
// hold m.mu waiting on the very load it paused.

// fileGrants records, per page session, the work directories a call opened
// local files from. Grants accumulate: each was named by a call.
type fileGrants struct {
	mu    sync.Mutex
	roots map[string][]string
}

func (g *fileGrants) grant(sessionID, root string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.roots == nil {
		g.roots = map[string][]string{}
	}
	for _, r := range g.roots[sessionID] {
		if r == root {
			return
		}
	}
	g.roots[sessionID] = append(g.roots[sessionID], root)
}

func (g *fileGrants) of(sessionID string) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.roots[sessionID]...)
}

func (g *fileGrants) drop(sessionID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.roots, sessionID)
}

// errNotPlaceable marks a file URL that names no local path this server can
// judge: another machine's share (file://host/...) or the opaque form file:x.
type errNotPlaceable string

func (e errNotPlaceable) Error() string { return string(e) }

// localPath returns the path a local-file URL names — file://, also inside
// view-source: — with local=true, and local=false for any other URL. A local
// URL that cannot be placed returns an error and is refused by every caller.
func localPath(rawURL string) (path string, local bool, err error) {
	// Chrome drops leading and trailing C0 controls and spaces, and tabs and
	// newlines anywhere, before it parses a URL ("fi\tle:///etc/hosts" is a
	// file URL to it). A URL carrying any control character is refused as
	// one that might be local rather than guessed at.
	s := strings.TrimFunc(rawURL, func(r rune) bool { return r <= ' ' })
	if strings.ContainsFunc(s, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return "", true, errNotPlaceable("the URL contains control characters")
	}
	for strings.HasPrefix(strings.ToLower(s), "view-source:") {
		s = strings.TrimSpace(s[len("view-source:"):])
	}
	if !strings.HasPrefix(strings.ToLower(s), "file:") {
		return "", false, nil
	}
	u, err := url.Parse(s)
	if err != nil || u.Opaque != "" {
		return "", true, errNotPlaceable("the file URL cannot be read as a path")
	}
	if h := strings.ToLower(u.Host); h != "" && h != "localhost" {
		return "", true, errNotPlaceable("a file URL naming another machine (" + u.Host + ") is not opened")
	}
	p := u.Path
	if p == "" {
		p = "/"
	}
	if runtime.GOOS == "windows" {
		// file:///C:/Users/x → C:\Users\x
		p = filepath.FromSlash(strings.TrimPrefix(p, "/"))
	}
	return filepath.Clean(p), true, nil
}

// place is where p is, or would be: the last of pathguard's forms — every
// link followed, a dangling one by its target — so whether the file exists
// never changes the answer.
func place(p string) string {
	f := pathguard.Forms(p)
	if len(f) == 0 {
		return p
	}
	return f[len(f)-1]
}

// judgeLocal is the one judgement for a local file: its place lies inside
// one of roots (the granted work directories), and pathguard's Local policy
// with this server's places passes it. It returns a details.reason and a
// sentence, or two empty strings.
func judgeLocal(r workdir.Resolver, roots []string, path string) (reason, why string) {
	where := place(path)
	inside := false
	for _, root := range roots {
		if root == where || under(root, where) {
			inside = true
			break
		}
	}
	if !inside {
		return "outside_work_dir", "a local file opens only inside the work directory the call names"
	}
	return r.LocalPath(path, where)
}

// checkLocalURL is the tool-argument layer for navigate_page and new_page. It
// returns the work directory to grant when rawURL is a local file that may be
// opened, "" when rawURL is not a local file, and a refusal otherwise.
// work_dir is resolved only for a local file: the argument, else the
// runtime's hint in _meta.
func (m *Manager) checkLocalURL(ctx context.Context, rawURL, workDirArg string) (string, error) {
	path, local, err := localPath(rawURL)
	if !local {
		return "", nil
	}
	if err != nil {
		return "", toolerr.Newf(toolerr.CodePathNotAllowed, "%s is refused: %v", rawURL, err).
			WithDetails(map[string]any{"reason": "outside_work_dir", "url": rawURL})
	}
	wsRoot, err := m.workDir(ctx, workDirArg)
	if err != nil {
		return "", err
	}
	if reason, why := judgeLocal(m.resolver(), []string{wsRoot}, path); why != "" {
		return "", toolerr.Newf(toolerr.CodePathNotAllowed, "%s is refused: %s", rawURL, why).
			WithDetails(map[string]any{"reason": reason, "url": rawURL, "work_dir": wsRoot})
	}
	return wsRoot, nil
}

// fileRequestAllowed is the interception's answer for one file:// load of a
// session: inside a work directory granted to that session, and passing
// Local. --block-local refuses them all.
func (m *Manager) fileRequestAllowed(sessionID, rawURL string) bool {
	if m.filter.blockLocal {
		return false
	}
	path, _, err := localPath(rawURL)
	if err != nil {
		return false
	}
	_, why := judgeLocal(m.resolver(), m.grants.of(sessionID), path)
	return why == ""
}

// refuseUngrantedLocal keeps a tool off a page whose document is a local file
// no grant of its session covers. The URL is the one Target.getTargets just
// reported (selectedPage refreshes it on every call).
func (m *Manager) refuseUngrantedLocal(p *pageState) error {
	path, local, err := localPath(p.url)
	if !local {
		return nil
	}
	if err == nil {
		if _, why := judgeLocal(m.resolver(), m.grants.of(p.sessionID), path); why == "" {
			return nil
		}
	}
	return toolerr.Newf(toolerr.CodePathNotAllowed,
		"the selected page shows a local file that no call opened under its work_dir (%s) — a page a script opened, "+
			"or one opened outside this server; navigate it elsewhere with navigate_page before using it", p.url).
		WithDetails(map[string]any{"reason": "outside_work_dir", "url": p.url})
}

// refuseUngrantedSession applies refuseUngrantedLocal to the page a collected
// record came from, for the tools that read records by id rather than
// through selectedPage (get_network_request, get_console_message). The page
// list is refreshed first, as selectedPage does, so the judgement is on the
// document shown now. A record whose page is gone has no document to judge.
func (m *Manager) refuseUngrantedSession(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	m.mu.Lock()
	if m.client != nil {
		if err := m.refreshPagesLocked(ctx); err != nil {
			m.mu.Unlock()
			return err
		}
	}
	var p *pageState
	for _, q := range m.pages {
		if q.sessionID == sessionID {
			p = q
			break
		}
	}
	var snapshot pageState
	if p != nil {
		snapshot = *p
	}
	m.mu.Unlock()
	if p == nil {
		return nil
	}
	return m.refuseUngrantedLocal(&snapshot)
}

// refuseUngrantedLoad refuses a collected local-file load no grant of its
// session covers: its response body is that file.
func (m *Manager) refuseUngrantedLoad(sessionID, rawURL string) error {
	path, local, err := localPath(rawURL)
	if !local {
		return nil
	}
	if err == nil {
		if _, why := judgeLocal(m.resolver(), m.grants.of(sessionID), path); why == "" {
			return nil
		}
	}
	return toolerr.Newf(toolerr.CodePathNotAllowed,
		"%s is a local file no call opened under its work_dir; its content is not returned", rawURL).
		WithDetails(map[string]any{"reason": "outside_work_dir", "url": rawURL})
}
