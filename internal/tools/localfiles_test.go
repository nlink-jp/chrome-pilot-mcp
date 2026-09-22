package tools

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/nlink-jp/chrome-pilot-mcp/internal/toolerr"
)

// ADR-0008: a local file opens only inside the call's work_dir.

func TestLocalPathReadsFileURLs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the paths below are Unix paths")
	}
	for _, c := range []struct {
		url, path string
		local     bool
		err       bool
	}{
		{"file:///a/b.html", "/a/b.html", true, false},
		{"FILE:///a/b.html", "/a/b.html", true, false},
		{"  file:///a/../c  ", "/c", true, false},
		{"file:///a%20b/c.txt", "/a b/c.txt", true, false},
		{"file://localhost/a", "/a", true, false},
		{"file://LOCALHOST/a", "/a", true, false},
		{"view-source:file:///a", "/a", true, false},
		{"VIEW-SOURCE:view-source:file:///a", "/a", true, false},
		{"file:///", "/", true, false},
		{"file://fileserver/share/x", "", true, true},
		{"file:relative", "", true, true},
		{"https://example.com/", "", false, false},
		{"view-source:https://example.com/", "", false, false},
		{"data:text/html,x", "", false, false},
		{"about:blank", "", false, false},
	} {
		path, local, err := localPath(c.url)
		if local != c.local || (err != nil) != c.err || (!c.err && path != c.path) {
			t.Errorf("localPath(%q) = %q, %v, %v; want %q, %v, err=%v", c.url, path, local, err, c.path, c.local, c.err)
		}
	}
}

// localFixture is a home, a work directory with a page in it, and a file
// outside the work directory.
func localFixture(t *testing.T) (home, work, page, outside string) {
	t.Helper()
	home = resolvedTempDir(t)
	t.Setenv("HOME", home)
	work = resolvedTempDir(t)
	page = filepath.Join(work, "report.html")
	mustWrite(t, page, "<p>report</p>")
	outside = filepath.Join(resolvedTempDir(t), "secret.txt")
	mustWrite(t, outside, "FAKE-SECRET")
	return home, work, page, outside
}

func TestNavigateOpensALocalFileOnlyUnderWorkDir(t *testing.T) {
	home, work, page, outside := localFixture(t)
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)

	if _, err := callTool(t, m.navigatePage, `{"url":"file://`+page+`","work_dir":`+quote(work)+`}`); err != nil {
		t.Fatalf("a page under work_dir: %v", err)
	}
	if got := m.grants.of("sess-T1"); len(got) != 1 || got[0] != work {
		t.Errorf("grants after opening a page under work_dir = %q, want [%s]", got, work)
	}
	navs := f.callCount("Page.navigate")

	for _, url := range []string{"file://" + outside, "view-source:file://" + outside, "file://" + work + "/../" + filepath.Base(filepath.Dir(outside)) + "/secret.txt"} {
		_, err := callTool(t, m.navigatePage, `{"url":`+quote(url)+`,"work_dir":`+quote(work)+`}`)
		wantPathRefused(t, err, "outside_work_dir")
	}
	_, err := callTool(t, m.newPage, `{"url":"file://`+outside+`","work_dir":`+quote(work)+`}`)
	wantPathRefused(t, err, "outside_work_dir")
	if f.callCount("Page.navigate") != navs || f.callCount("Target.createTarget") != 0 {
		t.Error("a refused local file was navigated to, or a tab was created for it")
	}

	// A credential location inside an accepted work directory is refused by
	// pathguard's reason.
	config := filepath.Join(home, ".config")
	mustWrite(t, filepath.Join(config, "gh", "hosts.yml"), "token")
	_, err = callTool(t, m.navigatePage, `{"url":"file://`+filepath.Join(config, "gh", "hosts.yml")+`","work_dir":`+quote(config)+`}`)
	wantPathRefused(t, err, "sensitive_path")
}

func TestNewPageGrantsItsOwnSession(t *testing.T) {
	_, work, page, _ := localFixture(t)
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	if _, err := callTool(t, m.newPage, `{"url":"file://`+page+`","work_dir":`+quote(work)+`}`); err != nil {
		t.Fatalf("new_page on a page under work_dir: %v", err)
	}
	if got := m.grants.of("sess-T2"); len(got) != 1 || got[0] != work {
		t.Errorf("grants of the new tab = %q, want [%s]", got, work)
	}
	if got := m.grants.of("sess-T1"); len(got) != 0 {
		t.Errorf("the other tab was granted too: %q", got)
	}
}

// The interception holds every file:// load of a session to its grants: a
// script's navigation, a subresource, an iframe — whatever the page asks for.
func TestTheInterceptionHoldsLocalFilesToTheGrants(t *testing.T) {
	_, work, page, outside := localFixture(t)
	f := newFakeChrome(t, "about:blank", "about:blank")
	m := newTestManager(t, Config{}, f)
	if _, err := callTool(t, m.navigatePage, `{"url":"file://`+page+`","work_dir":`+quote(work)+`}`); err != nil {
		t.Fatal(err)
	}
	paused := func(session, id, url string) {
		f.emit(session, "Fetch.requestPaused", map[string]any{"requestId": id, "request": map[string]any{"url": url}})
	}
	paused("sess-T1", "A", "file://"+filepath.Join(work, "style.css"))
	waitUntil(t, "a file under the grant continues", func() bool { return f.callCount("Fetch.continueRequest") == 1 })
	paused("sess-T1", "B", "file://"+outside)
	waitUntil(t, "a file outside the grant fails", func() bool { return f.callCount("Fetch.failRequest") == 1 })
	paused("sess-T2", "C", "file://"+filepath.Join(work, "style.css")) // a session no call granted
	waitUntil(t, "another session's file load fails", func() bool { return f.callCount("Fetch.failRequest") == 2 })
	paused("sess-T1", "D", "https://example.com/app.js")
	waitUntil(t, "the web is not held to the grants", func() bool { return f.callCount("Fetch.continueRequest") == 2 })
	// Inside a grant, pathguard's Local policy still applies to every load.
	paused("sess-T1", "E", "file://"+filepath.Join(work, ".env"))
	waitUntil(t, "a .env inside the grant fails", func() bool { return f.callCount("Fetch.failRequest") == 3 })
	for _, c := range f.callsOf("Fetch.failRequest") {
		if id := c.params["requestId"]; id == "A" || id == "D" {
			t.Errorf("request %v was failed", c.params["requestId"])
		}
	}
}

func TestBlockLocalRefusesEveryLocalLoad(t *testing.T) {
	_, work, _, _ := localFixture(t)
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{BlockLocal: true}, f)
	m.grants.grant("sess-T1", work)
	if m.fileRequestAllowed("sess-T1", "file://"+filepath.Join(work, "report.html")) {
		t.Error("--block-local let a granted local file load")
	}
}

// A page showing a local file no grant covers — a tab a script opened with
// window.open loads before it is attached — is used by nothing but
// navigate_page.
func TestAPageShowingAnUngrantedLocalFileIsOffLimits(t *testing.T) {
	_, work, page, outside := localFixture(t)
	f := newFakeChrome(t, "file://"+outside)
	m := newTestManager(t, Config{}, f)
	for name, call := range map[string]func() (any, error){
		"take_snapshot":   func() (any, error) { return callTool(t, m.takeSnapshot, `{}`) },
		"evaluate_script": func() (any, error) { return callTool(t, m.evaluateScript, `{"function":"() => 1"}`) },
		"take_screenshot": func() (any, error) { return callTool(t, m.takeScreenshot, `{"work_dir":`+quote(work)+`}`) },
	} {
		_, err := call()
		var te *toolerr.Error
		if !errors.As(err, &te) || te.Code != toolerr.CodePathNotAllowed {
			t.Errorf("%s on a page showing an ungranted local file: %v, want %s", name, err, toolerr.CodePathNotAllowed)
		}
	}
	// navigate_page moves it away, and the page is usable again.
	if _, err := callTool(t, m.navigatePage, `{"url":"https://example.com/"}`); err != nil {
		t.Fatalf("navigating away: %v", err)
	}
	if _, err := callTool(t, m.takeSnapshot, `{}`); err != nil {
		t.Errorf("take_snapshot after navigating away: %v", err)
	}
	// And a local file opened under a work_dir is usable.
	if _, err := callTool(t, m.navigatePage, `{"url":"file://`+page+`","work_dir":`+quote(work)+`}`); err != nil {
		t.Fatal(err)
	}
	if _, err := callTool(t, m.takeSnapshot, `{}`); err != nil {
		t.Errorf("take_snapshot on a granted local page: %v", err)
	}
}

// The interception decides while a tool call may hold m.mu waiting on the
// very load it paused; its decision must not wait for m.mu.
func TestTheInterceptionDoesNotWaitOnTheManagerLock(t *testing.T) {
	_, work, page, _ := localFixture(t)
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	if _, err := callTool(t, m.navigatePage, `{"url":"file://`+page+`","work_dir":`+quote(work)+`}`); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	done := make(chan bool, 1)
	go func() { done <- m.requestAllowed("sess-T1", "file://"+page) }()
	select {
	case ok := <-done:
		if !ok {
			t.Error("a granted local file was refused")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the interception's decision waited on m.mu")
	}
}

// view-source: shows what the URL inside it loads, so the host lists judge
// that URL too.
func TestViewSourceIsJudgedByItsInnerURL(t *testing.T) {
	f := newHostFilter(Config{AllowHosts: []string{"example.com"}})
	if ok, _ := f.urlAllowed("view-source:https://exfil.test/"); ok {
		t.Error("view-source: of a host outside the allow list was allowed")
	}
	if ok, _ := f.urlAllowed("view-source:https://example.com/"); !ok {
		t.Error("view-source: of an allowed host was refused")
	}
	b := newHostFilter(Config{BlockLocal: true})
	if ok, _ := b.urlAllowed("view-source:file:///etc/hosts"); ok {
		t.Error("--block-local allowed view-source:file://")
	}
}

func TestClosingAPageDropsItsGrants(t *testing.T) {
	_, work, page, _ := localFixture(t)
	f := newFakeChrome(t, "about:blank", "about:blank")
	m := newTestManager(t, Config{}, f)
	if _, err := callTool(t, m.navigatePage, `{"url":"file://`+page+`","work_dir":`+quote(work)+`}`); err != nil {
		t.Fatal(err)
	}
	if _, err := callTool(t, m.closePage, `{"pageIdx":0}`); err != nil {
		t.Fatal(err)
	}
	if got := m.grants.of("sess-T1"); len(got) != 0 {
		t.Errorf("grants of a closed page = %q", got)
	}
}

// showLocalFile makes the fake's first page show a local file behind the
// server's back — what a tab opened by a script, or by the user, looks like.
func (f *fakeChrome) showLocalFile(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pages[0].url = "file://" + path
}

// A page showing an ungranted local file is refused before it is attached:
// attaching would turn the console and network collectors on over it.
func TestAnUngrantedLocalPageIsNotAttached(t *testing.T) {
	_, _, _, outside := localFixture(t)
	f := newFakeChrome(t, "file://"+outside)
	m := newTestManager(t, Config{}, f)
	if _, err := callTool(t, m.takeSnapshot, `{}`); err == nil {
		t.Fatal("take_snapshot on an ungranted local page succeeded")
	}
	if n := f.callCount("Target.attachToTarget") + f.callCount("Runtime.enable") + f.callCount("Network.enable"); n != 0 {
		t.Errorf("the page was attached (%d attach/enable calls) before it was refused", n)
	}
}

// The tools that read collected records by id, not through selectedPage, hold
// to the same rule: nothing from a page showing an ungranted local file, and
// no body of an ungranted local-file load.
func TestRecordsReadByIDHoldToTheRule(t *testing.T) {
	_, work, page, outside := localFixture(t)
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	snapshotFirst(t, m) // attaches sess-T1 on about:blank

	f.emit("sess-T1", "Runtime.consoleAPICalled", map[string]any{
		"type": "log", "timestamp": 1.0, "args": []map[string]any{{"type": "string", "value": "FAKE-SECRET"}},
	})
	load := func(id, url string) {
		f.emit("sess-T1", "Network.requestWillBeSent", map[string]any{
			"requestId": id, "type": "Document", "request": map[string]any{"url": url, "method": "GET"},
		})
		f.emit("sess-T1", "Network.responseReceived", map[string]any{
			"requestId": id, "response": map[string]any{"status": 200, "mimeType": "text/plain"},
		})
		f.emit("sess-T1", "Network.loadingFinished", map[string]any{"requestId": id, "encodedDataLength": 10})
	}
	load("R1", "file://"+outside)
	load("R2", "https://example.com/")
	waitUntil(t, "records", func() bool {
		m.col.mu.Lock()
		defer m.col.mu.Unlock()
		return len(m.col.consoleMsgs) == 1 && len(m.col.netReqs) == 2 && m.col.netReqs[1].Finished
	})

	// The body of an ungranted local-file load is not returned; a web one is.
	_, err := callTool(t, m.getNetworkRequest, `{"reqid":1}`)
	wantPathRefused(t, err, "outside_work_dir")
	if _, err := callTool(t, m.getNetworkRequest, `{"reqid":2}`); err != nil {
		t.Errorf("a web request: %v", err)
	}
	if _, err := callTool(t, m.getConsoleMessage, `{"msgid":1}`); err != nil {
		t.Errorf("a console message from an ordinary page: %v", err)
	}

	// Once the page shows an ungranted local file, nothing collected from it
	// is read by id.
	f.showLocalFile(outside)
	_, _ = callTool(t, m.listPages, `{}`) // refresh the page list, as any page tool does
	_, err = callTool(t, m.getConsoleMessage, `{"msgid":1}`)
	wantPathRefused(t, err, "outside_work_dir")
	_, err = callTool(t, m.getNetworkRequest, `{"reqid":2}`)
	wantPathRefused(t, err, "outside_work_dir")

	// A granted local page reads as any other.
	if _, err := callTool(t, m.navigatePage, `{"url":"file://`+page+`","work_dir":`+quote(work)+`}`); err != nil {
		t.Fatal(err)
	}
	if _, err := callTool(t, m.getConsoleMessage, `{"msgid":1}`); err != nil {
		t.Errorf("a console message after moving to a granted page: %v", err)
	}
}

// handle_dialog still answers a dialog on such a page, so it is not left
// blocked, but its words are withheld.
func TestADialogOnAnUngrantedLocalPageIsAnsweredButNotRead(t *testing.T) {
	_, _, _, outside := localFixture(t)
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	snapshotFirst(t, m)
	f.showLocalFile(outside)
	_, _ = callTool(t, m.listPages, `{}`)
	f.emit("sess-T1", "Page.javascriptDialogOpening", map[string]any{"type": "alert", "message": "FAKE-SECRET"})
	waitUntil(t, "dialog tracked", func() bool { return m.openDialog("sess-T1") != nil })
	out, err := callTool(t, m.handleDialog, `{"action":"dismiss"}`)
	if err != nil {
		t.Fatalf("handle_dialog: %v", err)
	}
	res := out.(map[string]any)
	if res["message"] != "" || f.callCount("Page.handleJavaScriptDialog") != 1 {
		t.Errorf("handle_dialog on an ungranted local page = %v (handled %d times)", res, f.callCount("Page.handleJavaScriptDialog"))
	}
}

// Chrome drops tabs and newlines inside a URL, so "fi\tle:" is a file URL to
// it: a URL with control characters is refused as one that might be local.
func TestAURLWithControlCharactersIsRefused(t *testing.T) {
	_, work, _, outside := localFixture(t)
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	for _, url := range []string{"fi\tle://" + outside, "\x01file://" + outside, "file://" + outside + "\n"} {
		body, _ := json.Marshal(map[string]string{"url": url, "work_dir": work})
		_, err := callTool(t, m.navigatePage, string(body))
		var te *toolerr.Error
		if !errors.As(err, &te) || te.Code != toolerr.CodePathNotAllowed {
			t.Errorf("navigate_page(%q) = %v, want %s", url, err, toolerr.CodePathNotAllowed)
		}
	}
	if f.callCount("Page.navigate") != 0 {
		t.Error("a URL with control characters was navigated to")
	}
}
