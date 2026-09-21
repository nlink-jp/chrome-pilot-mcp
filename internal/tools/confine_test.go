package tools

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/chrome-pilot-mcp/internal/browser"
	"github.com/nlink-jp/chrome-pilot-mcp/internal/toolerr"
)

// These tests hold the rules ADR-0006 adds, each at the layer a caller
// reaches: the tool functions, driven against the fake Chrome.

func wantPathRefused(t *testing.T, err error, reason string) {
	t.Helper()
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Code != toolerr.CodePathNotAllowed {
		t.Fatalf("want %s, got %v", toolerr.CodePathNotAllowed, err)
	}
	if te.Details["reason"] != reason {
		t.Errorf("reason = %v, want %s", te.Details["reason"], reason)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func wantEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("something was written in %s: %v", dir, entries)
	}
}

// A work_dir may be the parent of this server's own directory, and the files
// under it are then the managed browser profiles' cookies and config.toml.
// Neither direction may reach them (the review of ADR-0006 found both open).
func TestFileArgumentsNeverReachTheServerOwnDirectory(t *testing.T) {
	own := serverDir(t)
	parent := filepath.Dir(own)
	mustWrite(t, filepath.Join(own, "profiles", "p", "Default", "Cookies"), "session")
	name := filepath.Base(own)

	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	snapshotFirst(t, m)
	_, err := callTool(t, m.uploadFile, `{"uid":"1_3","filePath":`+
		quote(filepath.Join(name, "profiles", "p", "Default", "Cookies"))+`,"work_dir":`+quote(parent)+`}`)
	wantPathRefused(t, err, "server_dir")
	if n := len(f.callsOf("DOM.setFileInputFiles")); n != 0 {
		t.Errorf("Chrome was handed a profile file (%d calls)", n)
	}

	_, err = callTool(t, m.screencastStart, `{"filePath":`+quote(filepath.Join(name, "x.gif"))+`,"work_dir":`+quote(parent)+`}`)
	wantPathRefused(t, err, "server_dir")
}

// A link from work_dir to a credential file is refused as a credential file.
// The name the caller gave (link.txt) is harmless; only the resolved form is
// on the blacklist, and the blacklist runs before the containment check — so
// this fails both if Sensitive sees only the given path and if containment
// runs first (it would say outside_work_dir).
func TestUploadRefusesALinkToACredentialFileAsSuch(t *testing.T) {
	work := resolvedTempDir(t)
	secrets := resolvedTempDir(t)
	mustWrite(t, filepath.Join(secrets, ".env"), "TOKEN=x")
	if err := os.Symlink(filepath.Join(secrets, ".env"), filepath.Join(work, "link.txt")); err != nil {
		t.Fatal(err)
	}
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	snapshotFirst(t, m)
	_, err := callTool(t, m.uploadFile, `{"uid":"1_3","filePath":"link.txt","work_dir":`+quote(work)+`}`)
	wantPathRefused(t, err, "sensitive_path")
}

// The check at start is not the only guard: if a directory on the path is
// swapped for a link out of work_dir while recording, the write at stop goes
// through the os.Root opened at start and is refused.
func TestScreencastStopRefusesALinkThatAppearedAfterStart(t *testing.T) {
	work := resolvedTempDir(t)
	elsewhere := resolvedTempDir(t)
	if err := os.Mkdir(filepath.Join(work, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	if _, err := callTool(t, m.screencastStart, `{"filePath":"sub/cast.gif","work_dir":`+quote(work)+`}`); err != nil {
		t.Fatalf("screencast_start: %v", err)
	}
	if err := os.Remove(filepath.Join(work, "sub")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(work, "sub")); err != nil {
		t.Fatal(err)
	}
	feedOneFrame(t, m, f)
	_, err := callTool(t, m.screencastStop, `{}`)
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Code != toolerr.CodeWorkspaceFailed {
		t.Errorf("stop through a swapped-in link: want %s, got %v", toolerr.CodeWorkspaceFailed, err)
	}
	wantEmpty(t, elsewhere)
}

// work_dir itself swapped for a link during the recording: the directory the
// root holds is no longer the one the path names, and the write is refused —
// nothing lands at the link's target, and nothing is written into the moved
// directory behind the caller's back (the third review).
func TestScreencastRefusesAWorkDirSwappedDuringTheRecording(t *testing.T) {
	work := resolvedTempDir(t)
	elsewhere := resolvedTempDir(t)
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	if _, err := callTool(t, m.screencastStart, `{"filePath":"cast.gif","work_dir":`+quote(work)+`}`); err != nil {
		t.Fatalf("screencast_start: %v", err)
	}
	moved := work + "-moved"
	if err := os.Rename(work, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, work); err != nil {
		t.Fatal(err)
	}
	feedOneFrame(t, m, f)
	_, err := callTool(t, m.screencastStop, `{}`)
	wantPathRefused(t, err, "outside_work_dir")
	wantEmpty(t, elsewhere)
	wantEmpty(t, moved)
}

// A hard link in work_dir to a file outside it is replaced, not written
// through: the GIF is written beside it and renamed into place.
func TestScreencastReplacesAHardLinkInsteadOfWritingThroughIt(t *testing.T) {
	work := resolvedTempDir(t)
	outside := filepath.Join(resolvedTempDir(t), "keep.gif")
	mustWrite(t, outside, "original")
	if err := os.Link(outside, filepath.Join(work, "hl.gif")); err != nil {
		t.Skipf("hard links unavailable here: %v", err)
	}
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	recordOneFrame(t, m, f, `{"filePath":"hl.gif","work_dir":`+quote(work)+`}`)
	if b, _ := os.ReadFile(outside); string(b) != "original" {
		t.Errorf("the file outside work_dir was overwritten: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(work, "hl.gif")); !bytes.HasPrefix(b, []byte("GIF8")) {
		t.Errorf("hl.gif is not the GIF: %q", b)
	}
}

// A dangling link out of work_dir is refused at start too: creating through
// it would create its target, outside.
func TestScreencastRefusesADanglingLinkOutAtStart(t *testing.T) {
	work := resolvedTempDir(t)
	elsewhere := resolvedTempDir(t)
	if err := os.Symlink(filepath.Join(elsewhere, "not-yet"), filepath.Join(work, "out")); err != nil {
		t.Fatal(err)
	}
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	_, err := callTool(t, m.screencastStart, `{"filePath":"out/cast.gif","work_dir":`+quote(work)+`}`)
	wantPathRefused(t, err, "outside_work_dir")
}

// An absolute filePath may use the spelling of work_dir the caller gave,
// before its symlinks were resolved. On a system where the temp directory has
// no symlink in its path the two spellings are one and this checks less.
func TestScreencastAcceptsTheCallersSpellingOfWorkDir(t *testing.T) {
	given := t.TempDir()
	resolved, err := filepath.EvalSymlinks(given)
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	out := recordOneFrame(t, m, f, `{"filePath":`+quote(filepath.Join(given, "c.gif"))+`,"work_dir":`+quote(resolved)+`}`)
	if path, _ := out["path"].(string); path != filepath.Join(resolved, "c.gif") {
		t.Errorf("path = %q, want %q", path, filepath.Join(resolved, "c.gif"))
	}
}

// take_screenshot writes through an os.Root too: a screenshots/ that is a
// link out of work_dir is refused rather than followed.
func TestScreenshotRefusesAScreenshotsLinkOutOfWorkDir(t *testing.T) {
	work := resolvedTempDir(t)
	elsewhere := resolvedTempDir(t)
	if err := os.Symlink(elsewhere, filepath.Join(work, "screenshots")); err != nil {
		t.Fatal(err)
	}
	f := newFakeChrome(t, "https://example.com/")
	f.overrides["Page.captureScreenshot"] = func(string, map[string]any) (any, string) {
		return map[string]any{"data": base64.StdEncoding.EncodeToString([]byte("PNG"))}, ""
	}
	m := newTestManager(t, Config{}, f)
	_, err := callTool(t, m.takeScreenshot, `{"work_dir":`+quote(work)+`}`)
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Code != toolerr.CodeWorkspaceFailed {
		t.Errorf("want %s, got %v", toolerr.CodeWorkspaceFailed, err)
	}
	wantEmpty(t, elsewhere)
}

// feedOneFrame delivers one frame to the recording on sess-T1.
func feedOneFrame(t *testing.T, m *Manager, f *fakeChrome) {
	t.Helper()
	frame := encodeTestJPEG(t, 20, 10, color.RGBA{0, 0, 255, 255})
	f.emit("sess-T1", "Page.screencastFrame", map[string]any{
		"data": base64.StdEncoding.EncodeToString(frame), "sessionId": 1,
		"metadata": map[string]any{"timestamp": 100.0},
	})
	waitUntil(t, "frame stored", func() bool {
		m.col.mu.Lock()
		defer m.col.mu.Unlock()
		sc := m.col.screencasts["sess-T1"]
		return sc != nil && len(sc.frames) == 1
	})
}

// caseInsensitive reports whether dir's filesystem folds case, the default
// for APFS; the spelling tests below mean nothing elsewhere.
func caseInsensitive(t *testing.T, dir string) bool {
	t.Helper()
	probe := filepath.Join(dir, "case-probe")
	if err := os.Mkdir(probe, 0o755); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(probe) }()
	_, err := os.Stat(filepath.Join(dir, "CASE-PROBE"))
	return err == nil
}

// The server's own directory under another spelling of its name is the same
// directory on a case-insensitive filesystem, and is refused as such: the
// comparison is by identity, not by name (the second review of ADR-0006).
func TestTheServerOwnDirectoryIsRefusedUnderAnySpelling(t *testing.T) {
	own := serverDir(t)
	parent := filepath.Dir(own)
	if !caseInsensitive(t, parent) {
		t.Skip("case-sensitive filesystem: another spelling is another directory")
	}
	mustWrite(t, filepath.Join(own, "profiles", "p", "Default", "Cookies"), "session")
	shout := strings.ToUpper(filepath.Base(own))

	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	snapshotFirst(t, m)
	_, err := callTool(t, m.uploadFile, `{"uid":"1_3","filePath":`+
		quote(filepath.Join(shout, "profiles", "p", "Default", "Cookies"))+`,"work_dir":`+quote(parent)+`}`)
	wantPathRefused(t, err, "server_dir")
	_, err = callTool(t, m.screencastStart, `{"filePath":`+quote(filepath.Join(shout, "x.gif"))+`,"work_dir":`+quote(parent)+`}`)
	wantPathRefused(t, err, "server_dir")
}

// A default output directory that is a link into the server's own directory
// — inside work_dir, so os.Root follows it — is refused too: every write
// checks where it lands, not only a caller-named path.
func TestScreenshotsLinkedIntoTheServerOwnDirectoryAreRefused(t *testing.T) {
	own := serverDir(t)
	parent := filepath.Dir(own)
	profile := filepath.Join(own, "profiles", "p", "Default")
	if err := os.MkdirAll(profile, 0o755); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(parent, profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(rel, filepath.Join(parent, "screenshots")); err != nil {
		t.Fatal(err)
	}
	f := newFakeChrome(t, "https://example.com/")
	f.overrides["Page.captureScreenshot"] = func(string, map[string]any) (any, string) {
		return map[string]any{"data": base64.StdEncoding.EncodeToString([]byte("PNG"))}, ""
	}
	m := newTestManager(t, Config{}, f)
	_, err = callTool(t, m.takeScreenshot, `{"work_dir":`+quote(parent)+`}`)
	wantPathRefused(t, err, "server_dir")
	wantEmpty(t, profile)
}

// Nothing planted at a guessable temporary name is written through: the
// temporary file has a random name and is created exclusively.
func TestAPlantedTemporaryNameIsNotWrittenThrough(t *testing.T) {
	work := resolvedTempDir(t)
	outside := filepath.Join(resolvedTempDir(t), "keep")
	mustWrite(t, outside, "original")
	for _, planted := range []string{".hl.gif.partial", ".hl.gif.tmp", "hl.gif.partial"} {
		if err := os.Link(outside, filepath.Join(work, planted)); err != nil {
			t.Skipf("hard links unavailable here: %v", err)
		}
	}
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	recordOneFrame(t, m, f, `{"filePath":"hl.gif","work_dir":`+quote(work)+`}`)
	if b, _ := os.ReadFile(outside); string(b) != "original" {
		t.Errorf("the file outside work_dir was overwritten through a temporary name: %q", b)
	}
}

// The profile of the Chrome being driven is protected wherever it is. A
// throwaway profile lives under the temp directory, which is a legitimate
// work_dir, and its name can be read from chrome://version (the third review
// uploaded its Cookies that way).
func TestTheDrivenBrowsersProfileIsProtected(t *testing.T) {
	temp := resolvedTempDir(t)
	profile := filepath.Join(temp, "chrome-pilot-mcp-profile-123")
	mustWrite(t, filepath.Join(profile, "Default", "Cookies"), "session")

	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	snapshotFirst(t, m)
	m.mu.Lock()
	m.profileDir = profile
	m.mu.Unlock()

	_, err := callTool(t, m.uploadFile, `{"uid":"1_3","filePath":"chrome-pilot-mcp-profile-123/Default/Cookies","work_dir":`+quote(temp)+`}`)
	wantPathRefused(t, err, "server_dir")
	_, err = callTool(t, m.uploadFile, `{"uid":"1_3","filePath":"Default/Cookies","work_dir":`+quote(profile)+`}`)
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Code != toolerr.CodeWorkDirDenied {
		t.Errorf("work_dir inside the driven profile: want %s, got %v", toolerr.CodeWorkDirDenied, err)
	}
}

// The user's own Chrome profile is protected too: a work_dir of
// ~/Library/Application Support would otherwise reach its cookies.
func TestTheUsersOwnChromeProfileIsProtected(t *testing.T) {
	serverDir(t) // HOME is a directory this test owns
	roots := browser.RealChromeProfileRoots()
	if len(roots) == 0 {
		t.Skip("no real Chrome profile location on this platform")
	}
	chrome := roots[0]
	mustWrite(t, filepath.Join(chrome, "Default", "Cookies"), "session")
	parent := filepath.Dir(chrome)
	rel, err := filepath.Rel(parent, filepath.Join(chrome, "Default", "Cookies"))
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	snapshotFirst(t, m)
	_, err = callTool(t, m.uploadFile, `{"uid":"1_3","filePath":`+quote(rel)+`,"work_dir":`+quote(parent)+`}`)
	wantPathRefused(t, err, "browser_profile")
}

// A work_dir inside the server's own directory under another spelling is
// refused as a work_dir, before anything is written — resolveWorkDir's own
// list compares names; the identity check behind it does not.
func TestAWorkDirInsideTheServerOwnDirectoryIsDeniedUnderAnySpelling(t *testing.T) {
	own := serverDir(t)
	if !caseInsensitive(t, filepath.Dir(own)) {
		t.Skip("case-sensitive filesystem: another spelling is another directory")
	}
	profile := filepath.Join(own, "profiles", "p", "Default")
	if err := os.MkdirAll(profile, 0o755); err != nil {
		t.Fatal(err)
	}
	shouted := filepath.Join(filepath.Dir(own), strings.ToUpper(filepath.Base(own)), "profiles", "p", "Default")
	f := newFakeChrome(t, "https://example.com/")
	m := newTestManager(t, Config{}, f)
	_, err := callTool(t, m.screencastStart, `{"work_dir":`+quote(shouted)+`}`)
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Code != toolerr.CodeWorkDirDenied {
		t.Fatalf("want %s, got %v", toolerr.CodeWorkDirDenied, err)
	}
	wantEmpty(t, profile)
}

// A directory swapped for a link into a protected place while recording is
// refused before anything is created there, not even an empty directory.
func TestNothingIsCreatedInAProtectedPlaceBeforeTheRefusal(t *testing.T) {
	own := serverDir(t)
	parent := filepath.Dir(own)
	profile := filepath.Join(own, "profiles", "p", "Default")
	if err := os.MkdirAll(profile, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(parent, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	if _, err := callTool(t, m.screencastStart, `{"filePath":"sub/new/x.gif","work_dir":`+quote(parent)+`}`); err != nil {
		t.Fatalf("screencast_start: %v", err)
	}
	if err := os.Remove(filepath.Join(parent, "sub")); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(parent, profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(rel, filepath.Join(parent, "sub")); err != nil {
		t.Fatal(err)
	}
	feedOneFrame(t, m, f)
	_, err = callTool(t, m.screencastStop, `{}`)
	wantPathRefused(t, err, "server_dir")
	wantEmpty(t, profile)
}

// A long file name still fits: the temporary name does not grow with it.
func TestALongFileNameStillFits(t *testing.T) {
	work := resolvedTempDir(t)
	name := strings.Repeat("n", 240) + ".gif"
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	out := recordOneFrame(t, m, f, `{"filePath":`+quote(name)+`,"work_dir":`+quote(work)+`}`)
	if path, _ := out["path"].(string); path != filepath.Join(work, name) {
		t.Errorf("path = %q", path)
	}
}

// Another server's throwaway profile in the same temp directory is protected
// too: each runtime runs its own chrome-pilot-mcp, and a killed one leaves
// its profile, cookies and all (the last review uploaded a sibling's).
func TestAnotherInstancesThrowawayProfileIsProtected(t *testing.T) {
	temp := resolvedTempDir(t)
	t.Setenv("TMPDIR", temp)
	mustWrite(t, filepath.Join(temp, "chrome-pilot-mcp-profile-999", "Default", "Cookies"), "session")
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	snapshotFirst(t, m)
	_, err := callTool(t, m.uploadFile, `{"uid":"1_3","filePath":"chrome-pilot-mcp-profile-999/Default/Cookies","work_dir":`+quote(temp)+`}`)
	wantPathRefused(t, err, "server_dir")
}

// A dangling link whose target climbs with ".." cannot be followed safely —
// joining it cancels a component by name before that component's own link is
// resolved — so it is refused rather than guessed at.
func TestADanglingLinkThatClimbsIsRefused(t *testing.T) {
	work := resolvedTempDir(t)
	// Written as a string: filepath.Join would clean the ".." away.
	if err := os.Symlink("somewhere/../not-there", filepath.Join(work, "out")); err != nil {
		t.Fatal(err)
	}
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	_, err := callTool(t, m.screencastStart, `{"filePath":"out/x.gif","work_dir":`+quote(work)+`}`)
	wantPathRefused(t, err, "outside_work_dir")
}
