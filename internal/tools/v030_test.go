package tools

import (
	"errors"
	"strings"
	"testing"

	"github.com/nlink-jp/chrome-pilot-mcp/internal/mcpserver"
	"github.com/nlink-jp/chrome-pilot-mcp/internal/toolerr"
)

// TestEmulateResetsUserAgent is the regression for the reported bug: a user
// agent override survived `emulate {}` while the viewport was reset by the
// same call, and the response omitted userAgent so the reset looked done.
func TestEmulateResetsUserAgent(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)

	if _, err := callTool(t, m.emulate, `{"userAgent":"test-UA/1.0","viewport":"1024x768"}`); err != nil {
		t.Fatalf("emulate: %v", err)
	}
	ua := f.callsOf("Emulation.setUserAgentOverride")
	if len(ua) != 1 || ua[0].params["userAgent"] != "test-UA/1.0" {
		t.Fatalf("setUserAgentOverride = %+v", ua)
	}

	out, err := callTool(t, m.emulate, `{}`)
	if err != nil {
		t.Fatalf("emulate reset: %v", err)
	}
	// The override must actually be cleared, not merely left out.
	ua = f.callsOf("Emulation.setUserAgentOverride")
	if len(ua) != 2 {
		t.Fatalf("reset must call setUserAgentOverride again, calls = %d", len(ua))
	}
	if ua[1].params["userAgent"] != "" {
		t.Errorf("reset should pass an empty userAgent, got %v", ua[1].params["userAgent"])
	}

	applied := out.(map[string]any)["applied"].(map[string]any)
	if applied["userAgent"] != "cleared" {
		t.Errorf("applied.userAgent = %v, want \"cleared\"", applied["userAgent"])
	}
	if applied["viewport"] != "cleared" {
		t.Errorf("applied.viewport = %v, want \"cleared\"", applied["viewport"])
	}
	if applied["geolocation"] != "cleared" {
		t.Errorf("applied.geolocation = %v, want \"cleared\"", applied["geolocation"])
	}
}

// TestEmulateAppliedIsExhaustive: every dimension is reported, so a reset is
// never mistaken for a no-op.
func TestEmulateAppliedIsExhaustive(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)

	out, err := callTool(t, m.emulate, `{}`)
	if err != nil {
		t.Fatalf("emulate: %v", err)
	}
	applied := out.(map[string]any)["applied"].(map[string]any)
	for _, key := range []string{
		"colorScheme", "cpuThrottlingRate", "networkConditions",
		"geolocation", "userAgent", "viewport", "extraHttpHeaders",
	} {
		if _, ok := applied[key]; !ok {
			t.Errorf("applied is missing %q: %v", key, applied)
		}
	}
	// Headers are the documented exception: untouched unless passed.
	if applied["extraHttpHeaders"] != "unchanged" {
		t.Errorf("extraHttpHeaders = %v, want \"unchanged\"", applied["extraHttpHeaders"])
	}
	if n := f.callCount("Network.setExtraHTTPHeaders"); n != 0 {
		t.Errorf("omitted extraHttpHeaders must not be written, calls = %d", n)
	}
}

func TestEmulateHeaderErrorShowsFormat(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)

	_, err := callTool(t, m.emulate, `{"extraHttpHeaders":"X-Test: hello"}`)
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Code != toolerr.CodeInvalidArguments {
		t.Fatalf("want invalid_arguments, got %v", err)
	}
	if !strings.Contains(te.Message, `{"X-Custom":"value"}`) {
		t.Errorf("error should show the expected shape: %q", te.Message)
	}
}

// TestSnapshotShowsCheckedState is the regression for the reported gap: a
// checkbox rendered identically whether or not it was ticked, so a toggle
// could not be verified from the snapshot alone.
func TestSnapshotShowsCheckedState(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	f.overrides["Accessibility.getFullAXTree"] = func(sessionID string, params map[string]any) (any, string) {
		return map[string]any{"nodes": []map[string]any{
			{"nodeId": "1", "role": map[string]any{"value": "RootWebArea"},
				"childIds": []string{"2", "3", "4", "5"}, "backendDOMNodeId": 100},
			{"nodeId": "2", "role": map[string]any{"value": "checkbox"}, "name": map[string]any{"value": "agree"},
				"properties": []map[string]any{{"name": "checked", "value": map[string]any{"value": "true"}}},
				"childIds":   []string{}, "backendDOMNodeId": 101},
			{"nodeId": "3", "role": map[string]any{"value": "checkbox"}, "name": map[string]any{"value": "spam"},
				"properties": []map[string]any{{"name": "checked", "value": map[string]any{"value": "false"}}},
				"childIds":   []string{}, "backendDOMNodeId": 102},
			{"nodeId": "4", "role": map[string]any{"value": "button"}, "name": map[string]any{"value": "Send"},
				"properties": []map[string]any{{"name": "disabled", "value": map[string]any{"value": true}}},
				"childIds":   []string{}, "backendDOMNodeId": 103},
			{"nodeId": "5", "role": map[string]any{"value": "button"}, "name": map[string]any{"value": "Cancel"},
				"properties": []map[string]any{{"name": "disabled", "value": map[string]any{"value": false}}},
				"childIds":   []string{}, "backendDOMNodeId": 104},
		}}, ""
	}
	m := newTestManager(t, Config{}, f)

	out, err := callTool(t, m.takeSnapshot, `{}`)
	if err != nil {
		t.Fatalf("take_snapshot: %v", err)
	}
	text := out.(mcpserver.RawResult).Content[0].Text

	if !strings.Contains(text, `checkbox "agree" checked=true`) {
		t.Errorf("ticked checkbox not reported:\n%s", text)
	}
	if !strings.Contains(text, `checkbox "spam" checked=false`) {
		t.Errorf("unticked checkbox not reported:\n%s", text)
	}
	if !strings.Contains(text, `button "Send" disabled=true`) {
		t.Errorf("disabled button not reported:\n%s", text)
	}
	// disabled=false is on nearly every node; reporting it would be noise.
	if strings.Contains(text, `"Cancel" disabled=false`) {
		t.Errorf("disabled=false should be omitted:\n%s", text)
	}
}

// TestNavigateSucceedsWhenLoadEventMissed: a document that reached
// readyState complete has navigated, whatever happened to the event.
func TestNavigateSucceedsWhenLoadEventMissed(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	f.overrides["Page.navigate"] = func(sessionID string, params map[string]any) (any, string) {
		return map[string]any{"frameId": "F1"}, "" // never fires loadEventFired
	}
	f.overrides["Runtime.evaluate"] = func(sessionID string, params map[string]any) (any, string) {
		expr, _ := params["expression"].(string)
		if strings.Contains(expr, extraMarkAttr) {
			return map[string]any{"result": map[string]any{"type": "object", "value": []any{}}}, ""
		}
		if expr == "document.readyState" {
			return map[string]any{"result": map[string]any{"type": "string", "value": "complete"}}, ""
		}
		return map[string]any{"result": map[string]any{"type": "undefined"}}, ""
	}
	m := newTestManager(t, Config{}, f)

	out, err := callTool(t, m.navigatePage, `{"url":"https://example.com/","timeout":50}`)
	if err != nil {
		t.Fatalf("a loaded document must not be reported as a timeout: %v", err)
	}
	res := out.(map[string]any)
	if res["loaded"] != true {
		t.Errorf("result = %v", res)
	}
	note, _ := res["note"].(string)
	if !strings.Contains(note, "readyState") {
		t.Errorf("note should explain the missed event: %q", note)
	}
}

// TestNavigateStillFailsWhenNotLoaded keeps the error path honest.
func TestNavigateStillFailsWhenNotLoaded(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	f.overrides["Page.navigate"] = func(sessionID string, params map[string]any) (any, string) {
		return map[string]any{"frameId": "F1"}, ""
	}
	f.overrides["Runtime.evaluate"] = func(sessionID string, params map[string]any) (any, string) {
		expr, _ := params["expression"].(string)
		if strings.Contains(expr, extraMarkAttr) {
			return map[string]any{"result": map[string]any{"type": "object", "value": []any{}}}, ""
		}
		return map[string]any{"result": map[string]any{"type": "string", "value": "loading"}}, ""
	}
	m := newTestManager(t, Config{}, f)

	_, err := callTool(t, m.navigatePage, `{"url":"https://slow.example/","timeout":50}`)
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Code != toolerr.CodeTimeout {
		t.Fatalf("want timeout, got %v", err)
	}
	if !strings.Contains(te.Message, "loading") {
		t.Errorf("error should include the readyState seen: %q", te.Message)
	}
}
