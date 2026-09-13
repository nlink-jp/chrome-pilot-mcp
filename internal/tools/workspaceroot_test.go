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

func TestTakeScreenshotWorkspaceRoot(t *testing.T) {
	imgBytes := []byte("PNG-PAYLOAD")
	f := newFakeChrome(t, "https://example.com/")
	f.overrides["Page.captureScreenshot"] = func(sessionID string, params map[string]any) (any, string) {
		return map[string]any{"data": base64.StdEncoding.EncodeToString(imgBytes)}, ""
	}
	configured := t.TempDir()
	m := newTestManager(t, Config{WorkspaceRoot: configured}, f)

	// A root that does not exist yet: the agent's session directory may be
	// named before anything has been written to it.
	perCall := filepath.Join(t.TempDir(), "session-work")

	out, err := callTool(t, m.takeScreenshot, `{"workspaceRoot":`+quote(perCall)+`}`)
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
	if strings.HasPrefix(meta.Path, configured) {
		t.Errorf("path %q fell back to the configured workspace", meta.Path)
	}
	got, err := os.ReadFile(meta.Path)
	if err != nil || string(got) != string(imgBytes) {
		t.Errorf("file contents mismatch: %v %q", err, got)
	}
}

func TestWorkspaceRootMustBeAbsolute(t *testing.T) {
	for _, root := range []string{"shots", "./shots", "~/shots"} {
		f := newFakeChrome(t, "about:blank")
		m := newTestManager(t, Config{WorkspaceRoot: t.TempDir()}, f)

		_, err := callTool(t, m.takeScreenshot, `{"workspaceRoot":`+quote(root)+`}`)
		var te *toolerr.Error
		if !errors.As(err, &te) || te.Code != toolerr.CodeInvalidArguments {
			t.Fatalf("take_screenshot %q: want invalid_arguments, got %v", root, err)
		}
		if !strings.Contains(te.Message, "absolute") {
			t.Errorf("message = %q, want it to name the requirement", te.Message)
		}

		// Refused at start, so no recording is left behind holding frames.
		_, err = callTool(t, m.screencastStart, `{"workspaceRoot":`+quote(root)+`}`)
		if !errors.As(err, &te) || te.Code != toolerr.CodeInvalidArguments {
			t.Fatalf("screencast_start %q: want invalid_arguments, got %v", root, err)
		}
		if _, err := callTool(t, m.screencastStop, `{}`); err == nil {
			t.Errorf("screencast_start should not have started a recording")
		}
	}
}

func TestScreencastWorkspaceRoot(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	configured := t.TempDir()
	m := newTestManager(t, Config{WorkspaceRoot: configured}, f)
	perCall := filepath.Join(t.TempDir(), "session-work")

	if _, err := callTool(t, m.screencastStart, `{"workspaceRoot":`+quote(perCall)+`}`); err != nil {
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

func TestScreencastFilePathBeatsWorkspaceRoot(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{WorkspaceRoot: t.TempDir()}, f)
	perCall := filepath.Join(t.TempDir(), "session-work")
	explicit := filepath.Join(t.TempDir(), "named", "cast.gif")

	if _, err := callTool(t, m.screencastStart,
		`{"filePath":`+quote(explicit)+`,"workspaceRoot":`+quote(perCall)+`}`); err != nil {
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
	if path, _ := out.(map[string]any)["path"].(string); path != explicit {
		t.Errorf("path = %q, want the explicit filePath %q", path, explicit)
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
