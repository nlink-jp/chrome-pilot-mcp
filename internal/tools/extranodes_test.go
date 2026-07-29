package tools

import (
	"errors"
	"strings"
	"testing"

	"github.com/nlink-jp/chrome-pilot-mcp/internal/mcpserver"
	"github.com/nlink-jp/chrome-pilot-mcp/internal/toolerr"
)

// extraFake wires a fakeChrome so the DOM pass reports two unnamed
// clickable elements: an empty box and an icon-only link.
func extraFake(t *testing.T) *fakeChrome {
	t.Helper()
	f := newFakeChrome(t, "https://example.com/")
	f.overrides["Runtime.evaluate"] = func(sessionID string, params map[string]any) (any, string) {
		expr, _ := params["expression"].(string)
		if !strings.Contains(expr, extraMarkAttr) {
			return map[string]any{"result": map[string]any{"type": "undefined"}}, ""
		}
		if strings.Contains(expr, "querySelectorAll('[") {
			return map[string]any{"result": map[string]any{"type": "boolean", "value": true}}, ""
		}
		return map[string]any{"result": map[string]any{"type": "object", "value": []map[string]any{
			{"tag": "div", "id": "close", "cls": "modal close", "w": 24, "h": 24, "href": ""},
			{"tag": "a", "id": "", "cls": "icon", "w": 40, "h": 40, "href": "/settings"},
		}}}, ""
	}
	f.overrides["DOM.querySelectorAll"] = func(sessionID string, params map[string]any) (any, string) {
		if params["selector"] != "["+extraMarkAttr+"]" {
			t.Errorf("selector = %v", params["selector"])
		}
		return map[string]any{"nodeIds": []int64{501, 502}}, ""
	}
	return f
}

// TestSnapshotRecoversUnnamedClickables is the regression for the gap found
// in real use: a clickable div with no text, aria-label or role never
// reaches the accessibility tree, which left it unreachable by uid.
func TestSnapshotRecoversUnnamedClickables(t *testing.T) {
	f := extraFake(t)
	m := newTestManager(t, Config{}, f)

	out, err := callTool(t, m.takeSnapshot, `{}`)
	if err != nil {
		t.Fatalf("take_snapshot: %v", err)
	}
	text := out.(mcpserver.RawResult).Content[0].Text

	if !strings.Contains(text, "Interactive elements not exposed in the accessibility tree") {
		t.Fatalf("missing extra section:\n%s", text)
	}
	// The default fake tree renders uids 1_1..1_3, so the extras continue
	// the numbering rather than colliding with it.
	if !strings.Contains(text, `uid=1_4 clickable <div#close.modal.close> 24x24`) {
		t.Errorf("missing empty click box:\n%s", text)
	}
	if !strings.Contains(text, `uid=1_5 clickable <a.icon> 40x40 href="/settings"`) {
		t.Errorf("missing icon link:\n%s", text)
	}

	// The uids must resolve to the node ids the DOM pass returned.
	m.mu.Lock()
	defer m.mu.Unlock()
	if got := m.uids["1_4"]; got.nodeID != 501 || got.backendNodeID != 0 {
		t.Errorf("uid 1_4 = %+v, want nodeID 501", got)
	}
	if got := m.uids["1_5"]; got.nodeID != 502 {
		t.Errorf("uid 1_5 = %+v, want nodeID 502", got)
	}
	// Accessibility-tree uids keep using backend node ids.
	if got := m.uids["1_2"]; got.backendNodeID != 101 || got.nodeID != 0 {
		t.Errorf("uid 1_2 = %+v, want backendNodeID 101", got)
	}
}

// TestRecoveredNodeIsClickable checks the recovered uid drives input: CDP
// takes a nodeId just as well as a backendNodeId.
func TestRecoveredNodeIsClickable(t *testing.T) {
	f := extraFake(t)
	m := newTestManager(t, Config{}, f)
	if _, err := callTool(t, m.takeSnapshot, `{}`); err != nil {
		t.Fatal(err)
	}

	if _, err := callTool(t, m.click, `{"uid":"1_4"}`); err != nil {
		t.Fatalf("click recovered element: %v", err)
	}
	box := f.callsOf("DOM.getBoxModel")
	last := box[len(box)-1].params
	if last["nodeId"] != 501.0 {
		t.Errorf("getBoxModel params = %v, want nodeId 501", last)
	}
	if _, ok := last["backendNodeId"]; ok {
		t.Errorf("must not send both node references: %v", last)
	}
}

// TestExtraNodeCorrelationMismatchIsSafe: if the marked elements and the
// resolved node ids disagree, no uid may be handed out — a wrong mapping
// would silently click the wrong element.
func TestExtraNodeCorrelationMismatchIsSafe(t *testing.T) {
	f := extraFake(t)
	f.overrides["DOM.querySelectorAll"] = func(sessionID string, params map[string]any) (any, string) {
		return map[string]any{"nodeIds": []int64{501}}, "" // one short
	}
	m := newTestManager(t, Config{}, f)

	out, err := callTool(t, m.takeSnapshot, `{}`)
	if err != nil {
		t.Fatalf("snapshot should still succeed: %v", err)
	}
	text := out.(mcpserver.RawResult).Content[0].Text
	if strings.Contains(text, "Interactive elements not exposed") {
		t.Errorf("must not list extras on mismatch:\n%s", text)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for uid, target := range m.uids {
		if target.nodeID != 0 {
			t.Errorf("uid %s got a nodeID despite the mismatch", uid)
		}
	}
}

// TestExtraPassFailureDoesNotFailSnapshot: the DOM pass is supplementary.
func TestExtraPassFailureDoesNotFailSnapshot(t *testing.T) {
	f := newFakeChrome(t, "https://example.com/")
	f.overrides["Runtime.evaluate"] = func(sessionID string, params map[string]any) (any, string) {
		return nil, "Runtime.evaluate is unavailable"
	}
	m := newTestManager(t, Config{}, f)

	out, err := callTool(t, m.takeSnapshot, `{}`)
	if err != nil {
		t.Fatalf("snapshot must survive a failing extra pass: %v", err)
	}
	if !strings.Contains(out.(mcpserver.RawResult).Content[0].Text, "uid=1_1 RootWebArea") {
		t.Errorf("accessibility tree missing")
	}
}

// TestMarkersAreCleanedUp: the temporary attribute must not be left behind
// on the page.
func TestMarkersAreCleanedUp(t *testing.T) {
	f := extraFake(t)
	m := newTestManager(t, Config{}, f)
	if _, err := callTool(t, m.takeSnapshot, `{}`); err != nil {
		t.Fatal(err)
	}

	var sawCleanup bool
	for _, c := range f.callsOf("Runtime.evaluate") {
		expr, _ := c.params["expression"].(string)
		if strings.Contains(expr, "removeAttribute") && strings.Contains(expr, "querySelectorAll('["+extraMarkAttr) {
			sawCleanup = true
		}
	}
	if !sawCleanup {
		t.Errorf("no cleanup pass removed the %s markers", extraMarkAttr)
	}
}

// TestStaleUIDAfterNewSnapshot pins that uids do not survive a re-snapshot,
// so an agent can never act on a stale mapping.
func TestStaleUIDAfterNewSnapshot(t *testing.T) {
	f := extraFake(t)
	m := newTestManager(t, Config{}, f)
	if _, err := callTool(t, m.takeSnapshot, `{}`); err != nil {
		t.Fatal(err)
	}
	if _, err := callTool(t, m.takeSnapshot, `{}`); err != nil {
		t.Fatal(err)
	}

	_, err := callTool(t, m.click, `{"uid":"1_4"}`)
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Code != toolerr.CodeElementNotFound {
		t.Fatalf("stale uid should be element_not_found, got %v", err)
	}
}
