package tools

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/chrome-pilot-mcp/internal/mcpserver"
	"github.com/nlink-jp/chrome-pilot-mcp/internal/toolerr"
)

// The per-call root exists so an agent gets back a path it can open
// (ADR-0004); these tests pin that it beats the configured workspace, that
// it is created, and that an unusable root is refused on the call that
// supplied it rather than when the file is written.

func TestTakeScreenshotWorkDir(t *testing.T) {
	imgBytes := []byte("PNG-PAYLOAD")
	f := newFakeChrome(t, "https://example.com/")
	f.overrides["Page.captureScreenshot"] = func(sessionID string, params map[string]any) (any, string) {
		return map[string]any{"data": base64.StdEncoding.EncodeToString(imgBytes)}, ""
	}
	m := newTestManager(t, Config{}, f)

	// The caller's own directory, which always exists: the server no longer
	// creates one, so a path that is not there is a typo (ADR-0005).
	perCall := filepath.Join(t.TempDir(), "session-work")
	if err := os.MkdirAll(perCall, 0o755); err != nil {
		t.Fatal(err)
	}
	// Compare against the resolved spelling: the server validates the work
	// directory down to it and builds every path it returns from that.
	if resolved, err := filepath.EvalSymlinks(perCall); err == nil {
		perCall = resolved
	}

	out, err := callTool(t, m.takeScreenshot, `{"work_dir":`+quote(perCall)+`}`)
	if err != nil {
		t.Fatalf("take_screenshot: %v", err)
	}
	var meta struct {
		Path string `json:"path"`
	}
	raw := out.(mcpserver.RawResult)
	if err := json.Unmarshal([]byte(raw.Content[0].Text), &meta); err != nil {
		t.Fatalf("meta not JSON: %v", err)
	}
	if want := filepath.Join(perCall, "screenshots"); filepath.Dir(meta.Path) != want {
		t.Errorf("path = %q, want a file in %q", meta.Path, want)
	}
	got, err := os.ReadFile(meta.Path)
	if err != nil || string(got) != string(imgBytes) {
		t.Errorf("file contents mismatch: %v %q", err, got)
	}
}

func TestWorkDirMustBeAbsolute(t *testing.T) {
	for _, root := range []string{"shots", "./shots", "~/shots"} {
		f := newFakeChrome(t, "about:blank")
		m := newTestManager(t, Config{}, f)

		_, err := callTool(t, m.takeScreenshot, `{"work_dir":`+quote(root)+`}`)
		var te *toolerr.Error
		if !errors.As(err, &te) || te.Code != toolerr.CodeWorkDirInvalid {
			t.Fatalf("take_screenshot %q: want work_dir_invalid, got %v", root, err)
		}
		if !strings.Contains(te.Message, "absolute") {
			t.Errorf("message = %q, want it to name the requirement", te.Message)
		}

		// Refused at start, so no recording is left behind holding frames.
		_, err = callTool(t, m.screencastStart, `{"work_dir":`+quote(root)+`}`)
		if !errors.As(err, &te) || te.Code != toolerr.CodeWorkDirInvalid {
			t.Fatalf("screencast_start %q: want work_dir_invalid, got %v", root, err)
		}
		if _, err := callTool(t, m.screencastStop, `{}`); err == nil {
			t.Errorf("screencast_start should not have started a recording")
		}
	}
}

func TestScreencastWorkDir(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	perCall := filepath.Join(t.TempDir(), "session-work")
	if err := os.MkdirAll(perCall, 0o755); err != nil {
		t.Fatal(err)
	}
	// Compare against the resolved spelling: the server validates the work
	// directory down to it and builds every path it returns from that.
	if resolved, err := filepath.EvalSymlinks(perCall); err == nil {
		perCall = resolved
	}

	if _, err := callTool(t, m.screencastStart, `{"work_dir":`+quote(perCall)+`}`); err != nil {
		t.Fatalf("screencast_start: %v", err)
	}
	frame := encodeTestJPEG(t, 20, 10, color.RGBA{255, 0, 0, 255})
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

	out, err := callTool(t, m.screencastStop, `{}`)
	if err != nil {
		t.Fatalf("screencast_stop: %v", err)
	}
	path, _ := out.(map[string]any)["path"].(string)
	if want := filepath.Join(perCall, "screencasts"); filepath.Dir(path) != want {
		t.Errorf("path = %q, want a file in %q", path, want)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("gif not written: %v", err)
	}
}

// recordOneFrame starts a screencast with args, feeds it one frame and stops
// it, returning stop's result.
func recordOneFrame(t *testing.T, m *Manager, f *fakeChrome, args string) map[string]any {
	t.Helper()
	if _, err := callTool(t, m.screencastStart, args); err != nil {
		t.Fatalf("screencast_start: %v", err)
	}
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
	out, err := callTool(t, m.screencastStop, `{}`)
	if err != nil {
		t.Fatalf("screencast_stop: %v", err)
	}
	return out.(map[string]any)
}

// A filePath names where under work_dir the GIF goes (ADR-0006): relative to
// it, or absolute inside it in either spelling of the directory.
func TestScreencastFilePathLandsUnderWorkDir(t *testing.T) {
	perCall := resolvedTempDir(t)
	cases := []struct{ name, filePath, want string }{
		{"relative, new subdirectory", "named/cast.gif", filepath.Join(perCall, "named", "cast.gif")},
		{"absolute inside", filepath.Join(perCall, "abs.gif"), filepath.Join(perCall, "abs.gif")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeChrome(t, "about:blank")
			m := newTestManager(t, Config{}, f)
			out := recordOneFrame(t, m, f, `{"filePath":`+quote(tc.filePath)+`,"work_dir":`+quote(perCall)+`}`)
			if path, _ := out["path"].(string); path != tc.want {
				t.Errorf("path = %q, want %q", path, tc.want)
			}
			if _, err := os.Stat(tc.want); err != nil {
				t.Errorf("gif not written: %v", err)
			}
		})
	}
}

// Anything that would put the GIF outside work_dir is refused at
// screencast_start, before a frame is collected (ADR-0006, organization
// ADR-021 §7: writes land only under work_dir).
func TestScreencastFilePathOutsideWorkDirIsRefusedAtStart(t *testing.T) {
	perCall := resolvedTempDir(t)
	elsewhere := resolvedTempDir(t)
	if err := os.Symlink(elsewhere, filepath.Join(perCall, "out")); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, filePath string }{
		{"absolute outside", filepath.Join(elsewhere, "cast.gif")},
		{"climbs out", "../cast.gif"},
		{"through a link out of work_dir", "out/cast.gif"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeChrome(t, "about:blank")
			m := newTestManager(t, Config{}, f)
			_, err := callTool(t, m.screencastStart, `{"filePath":`+quote(tc.filePath)+`,"work_dir":`+quote(perCall)+`}`)
			var te *toolerr.Error
			if !errors.As(err, &te) || te.Code != toolerr.CodePathNotAllowed || te.Details["reason"] != "outside_work_dir" {
				t.Fatalf("want path_not_allowed/outside_work_dir, got %v", err)
			}
			if n := f.callCount("Page.startScreencast"); n != 0 {
				t.Errorf("recording started anyway (%d calls)", n)
			}
		})
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Errorf("something was written outside work_dir: %v", entries)
	}
}

// quote renders a path as a JSON string so a Windows-style separator or a
// space in a temp directory cannot break the argument literal.
func quote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// resolvedTempDir is a work directory in the spelling the server will report,
// so a test can compare paths without caring that /var is a symlink here.
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
