package tools

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/chrome-pilot-mcp/internal/mcpserver"
	"github.com/nlink-jp/chrome-pilot-mcp/internal/toolerr"
)

// dialogOnInput makes the fake behave like a real renderer blocked by a
// JavaScript dialog: the input call never gets a reply, and the dialog
// event arrives instead.
func dialogOnInput(t *testing.T, f *fakeChrome, method string) {
	t.Helper()
	f.overrides[method] = func(sessionID string, params map[string]any) (any, string) {
		if typ, _ := params["type"].(string); typ == "mouseMoved" {
			return map[string]any{}, "" // the move lands before the dialog
		}
		go f.emit(sessionID, "Page.javascriptDialogOpening",
			map[string]any{"type": "confirm", "message": "proceed?"})
		return nil, noReply // the renderer is blocked; no reply ever comes
	}
}

// TestClickOnDialogReturnsPromptly is the regression for the bug found in
// real use: clicking a button that opens confirm() reported a 30s timeout
// even though the click had worked.
func TestClickOnDialogReturnsPromptly(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	snapshotFirst(t, m)
	dialogOnInput(t, f, "Input.dispatchMouseEvent")

	start := time.Now()
	out, err := callTool(t, m.click, `{"uid":"1_2"}`)
	if err != nil {
		t.Fatalf("click must not fail when a dialog opens: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("click took %v; it must return as soon as the dialog opens", elapsed)
	}

	body := out.(mcpserver.RawResult).Content[0].Text
	var res map[string]any
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatalf("result not JSON: %s", body)
	}
	if res["clicked"] != "1_2" {
		t.Errorf("the click still happened and must be reported: %v", res)
	}
	dlg, ok := res["dialogOpen"].(map[string]any)
	if !ok || dlg["type"] != "confirm" || dlg["message"] != "proceed?" {
		t.Errorf("dialogOpen = %v", res["dialogOpen"])
	}
	if note, _ := res["note"].(string); !strings.Contains(note, "handle_dialog") {
		t.Errorf("note should point at handle_dialog: %q", note)
	}
}

// TestClickSkipsSnapshotWhileDialogOpen: a snapshot would block on the same
// dialog, so includeSnapshot must be ignored in that case.
func TestClickSkipsSnapshotWhileDialogOpen(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	snapshotFirst(t, m)
	dialogOnInput(t, f, "Input.dispatchMouseEvent")

	out, err := callTool(t, m.click, `{"uid":"1_2","includeSnapshot":true}`)
	if err != nil {
		t.Fatalf("click: %v", err)
	}
	if blocks := out.(mcpserver.RawResult).Content; len(blocks) != 1 {
		t.Errorf("expected only the result block while blocked, got %d", len(blocks))
	}
}

// TestSecondActionWhileDialogOpen: once a dialog is known to be open, the
// next action must report it immediately instead of dispatching into a
// blocked renderer.
func TestSecondActionWhileDialogOpen(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	snapshotFirst(t, m)

	f.emit("sess-T1", "Page.javascriptDialogOpening",
		map[string]any{"type": "alert", "message": "hi"})
	waitUntil(t, "dialog tracked", func() bool { return m.openDialog("sess-T1") != nil })

	before := f.callCount("Input.dispatchMouseEvent")
	out, err := callTool(t, m.clickAt, `{"x":5,"y":5}`)
	if err != nil {
		t.Fatalf("click_at: %v", err)
	}
	if f.callCount("Input.dispatchMouseEvent") != before {
		t.Errorf("must not dispatch input into a blocked renderer")
	}
	if !strings.Contains(out.(mcpserver.RawResult).Content[0].Text, `"type":"alert"`) {
		t.Errorf("result should describe the open dialog: %s", out.(mcpserver.RawResult).Content[0].Text)
	}
}

// TestEvaluateScriptDialogGuard: a script calling confirm() blocks too.
func TestEvaluateScriptDialogGuard(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	snapshotFirst(t, m)

	f.overrides["Runtime.evaluate"] = func(sessionID string, params map[string]any) (any, string) {
		expr, _ := params["expression"].(string)
		if strings.Contains(expr, extraMarkAttr) {
			return map[string]any{"result": map[string]any{"type": "object", "value": []any{}}}, ""
		}
		go f.emit(sessionID, "Page.javascriptDialogOpening",
			map[string]any{"type": "prompt", "message": "name?"})
		return nil, noReply
	}

	start := time.Now()
	out, err := callTool(t, m.evaluateScript, `{"function":"() => confirm('x')"}`)
	if err != nil {
		t.Fatalf("evaluate_script: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("evaluate_script took %v", elapsed)
	}
	res := out.(map[string]any)
	if res["dialogOpen"] == nil {
		t.Errorf("result should report the dialog: %v", res)
	}
}

// TestNormalClickUnaffected guards against the guard itself introducing a
// regression on the happy path.
func TestNormalClickUnaffected(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	snapshotFirst(t, m)

	out, err := callTool(t, m.click, `{"uid":"1_2"}`)
	if err != nil {
		t.Fatalf("click: %v", err)
	}
	body := out.(mcpserver.RawResult).Content[0].Text
	if strings.Contains(body, "dialogOpen") {
		t.Errorf("no dialog was open; result must not mention one: %s", body)
	}
	if n := f.callCount("Input.dispatchMouseEvent"); n != 3 {
		t.Errorf("mouse events = %d, want 3", n)
	}
}

// TestActionsWithDialogAlreadyOpenFailFast is the regression for the gap the
// final test run found: v0.3.0 guarded input dispatch, but a click first
// resolves the element's geometry, and DOM.getBoxModel blocks on a dialog
// just as hard — the click waited out its whole 30s timeout instead of
// saying a dialog was open.
func TestActionsWithDialogAlreadyOpenFailFast(t *testing.T) {
	tools := []struct {
		name string
		call func(m *Manager) (any, error)
	}{
		{"click", func(m *Manager) (any, error) { return callTool(t, m.click, `{"uid":"1_2"}`) }},
		{"hover", func(m *Manager) (any, error) { return callTool(t, m.hover, `{"uid":"1_2"}`) }},
		{"fill", func(m *Manager) (any, error) { return callTool(t, m.fill, `{"uid":"1_3","value":"x"}`) }},
		{"take_snapshot", func(m *Manager) (any, error) { return callTool(t, m.takeSnapshot, `{}`) }},
		{"take_screenshot", func(m *Manager) (any, error) { return callTool(t, m.takeScreenshot, `{}`) }},
		{"evaluate_script", func(m *Manager) (any, error) {
			return callTool(t, m.evaluateScript, `{"function":"() => 1"}`)
		}},
		{"upload_file", func(m *Manager) (any, error) {
			return callTool(t, m.uploadFile, `{"uid":"1_3","filePath":"`+t.TempDir()+`"}`)
		}},
	}
	for _, tc := range tools {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeChrome(t, "about:blank")
			m := newTestManager(t, Config{WorkspaceRoot: t.TempDir()}, f)
			snapshotFirst(t, m) // attaches sess-T1 and seeds uids

			f.emit("sess-T1", "Page.javascriptDialogOpening",
				map[string]any{"type": "confirm", "message": "blocked?"})
			waitUntil(t, "dialog tracked", func() bool { return m.openDialog("sess-T1") != nil })

			start := time.Now()
			_, err := tc.call(m)
			if elapsed := time.Since(start); elapsed > 3*time.Second {
				t.Errorf("%s took %v; an open dialog must be reported, not waited out", tc.name, elapsed)
			}
			var te *toolerr.Error
			if !errors.As(err, &te) || te.Code != toolerr.CodeDialogOpen {
				t.Fatalf("%s: want dialog_open, got %v", tc.name, err)
			}
			if !strings.Contains(te.Message, "handle_dialog") {
				t.Errorf("%s: error should point at handle_dialog: %q", tc.name, te.Message)
			}
			if te.Details["dialogType"] != "confirm" {
				t.Errorf("%s: details should name the dialog: %v", tc.name, te.Details)
			}
		})
	}
}
