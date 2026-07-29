package tools

import (
	"errors"
	"strings"
	"testing"

	"github.com/nlink-jp/chrome-pilot-mcp/internal/toolerr"
)

// ---- wait_for selector support ----

func TestWaitExpression(t *testing.T) {
	tests := []struct {
		name, text, selector, state string
		wantContains                []string
		wantDescribe                string
		wantErr                     bool
	}{
		{name: "text", text: "hello", wantContains: []string{"innerText.includes", `"hello"`}, wantDescribe: "text"},
		{name: "selector default", selector: ".spinner", wantContains: []string{"querySelector", `".spinner"`, "getBoundingClientRect"}, wantDescribe: "visible"},
		{name: "hidden", selector: ".spinner", state: "hidden", wantContains: []string{"return true"}, wantDescribe: "hidden"},
		{name: "present", selector: "#x", state: "present", wantContains: []string{`!!document.querySelector("#x")`}, wantDescribe: "present"},
		{name: "absent", selector: "#x", state: "absent", wantContains: []string{`!document.querySelector("#x")`}, wantDescribe: "absent"},
		{name: "bad state", selector: "#x", state: "gone", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expr, describe, err := waitExpression(tt.text, tt.selector, tt.state)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("waitExpression: %v", err)
			}
			if describe != tt.wantDescribe {
				t.Errorf("describe = %q, want %q", describe, tt.wantDescribe)
			}
			for _, want := range tt.wantContains {
				if !strings.Contains(expr, want) {
					t.Errorf("expression missing %q:\n%s", want, expr)
				}
			}
		})
	}
}

// TestWaitForSelectorQuotesInput guards against a selector breaking out of
// the generated expression.
func TestWaitForSelectorQuotesInput(t *testing.T) {
	expr, _, err := waitExpression("", `a"]),script`, "present")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(expr, `"a"]`) {
		t.Errorf("selector was not escaped: %s", expr)
	}
	if !strings.Contains(expr, `"a\"]),script"`) {
		t.Errorf("expected a JSON-escaped selector, got: %s", expr)
	}
}

func TestWaitForSelectorHidden(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	polls := 0
	f.overrides["Runtime.evaluate"] = func(sessionID string, params map[string]any) (any, string) {
		expr, _ := params["expression"].(string)
		if strings.Contains(expr, extraMarkAttr) {
			return map[string]any{"result": map[string]any{"type": "object", "value": []any{}}}, ""
		}
		if !strings.Contains(expr, ".spinner") {
			t.Errorf("unexpected expression: %s", expr)
		}
		polls++
		return map[string]any{"result": map[string]any{"type": "boolean", "value": polls >= 2}}, ""
	}
	m := newTestManager(t, Config{}, f)

	out, err := callTool(t, m.waitFor, `{"selector":".spinner","state":"hidden"}`)
	if err != nil {
		t.Fatalf("wait_for: %v", err)
	}
	res := out.(map[string]any)
	if res["found"] != true || res["selector"] != ".spinner" || res["state"] != "hidden" {
		t.Errorf("result = %v", res)
	}
}

func TestWaitForSelectorTimeoutMessage(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	f.overrides["Runtime.evaluate"] = func(sessionID string, params map[string]any) (any, string) {
		expr, _ := params["expression"].(string)
		if strings.Contains(expr, extraMarkAttr) {
			return map[string]any{"result": map[string]any{"type": "object", "value": []any{}}}, ""
		}
		return map[string]any{"result": map[string]any{"type": "boolean", "value": false}}, ""
	}
	m := newTestManager(t, Config{}, f)

	_, err := callTool(t, m.waitFor, `{"selector":"#never","state":"visible","timeout":50}`)
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Code != toolerr.CodeTimeout {
		t.Fatalf("want timeout, got %v", err)
	}
	if !strings.Contains(te.Message, "#never") || !strings.Contains(te.Message, "visible") {
		t.Errorf("message should name the selector and state: %q", te.Message)
	}
}

func TestWaitForArgumentValidation(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)

	if _, err := callTool(t, m.waitFor, `{}`); err == nil {
		t.Errorf("neither text nor selector should fail")
	}
	if _, err := callTool(t, m.waitFor, `{"text":"a","selector":"#b"}`); err == nil {
		t.Errorf("both text and selector should fail")
	}
}

// ---- since cursors ----

func TestConsoleSinceCursor(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	snapshotFirst(t, m)

	emit := func(text string) {
		f.emit("sess-T1", "Runtime.consoleAPICalled", map[string]any{
			"type": "log", "timestamp": 1.0,
			"args": []map[string]any{{"type": "string", "value": text}},
		})
	}
	emit("first")
	emit("second")
	waitUntil(t, "two messages", func() bool {
		m.col.mu.Lock()
		defer m.col.mu.Unlock()
		return len(m.col.consoleMsgs) == 2
	})

	out, err := callTool(t, m.listConsoleMessages, `{}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	res := out.(map[string]any)
	last := res["lastMsgId"].(int)
	if res["total"] != 2 || last != 2 {
		t.Fatalf("first listing = %v", res)
	}

	// Nothing new yet.
	out, _ = callTool(t, m.listConsoleMessages, `{"sinceMsgId":2}`)
	res = out.(map[string]any)
	if res["total"] != 0 {
		t.Errorf("expected no new messages, got %v", res["total"])
	}
	if res["lastMsgId"].(int) != last {
		t.Errorf("lastMsgId must survive an empty result: %v", res["lastMsgId"])
	}

	emit("third")
	waitUntil(t, "third message", func() bool {
		m.col.mu.Lock()
		defer m.col.mu.Unlock()
		return len(m.col.consoleMsgs) == 3
	})
	out, _ = callTool(t, m.listConsoleMessages, `{"sinceMsgId":2}`)
	res = out.(map[string]any)
	msgs := res["messages"].([]*consoleMsg)
	if res["total"] != 1 || len(msgs) != 1 || msgs[0].Text != "third" {
		t.Errorf("since-cursor listing = %v", res)
	}
}

func TestNetworkSinceCursorAndFailedOnly(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	snapshotFirst(t, m)

	send := func(id, url string) {
		f.emit("sess-T1", "Network.requestWillBeSent", map[string]any{
			"requestId": id, "type": "Fetch",
			"request": map[string]any{"url": url, "method": "GET"},
		})
	}
	send("R1", "https://ok.example/a")
	f.emit("sess-T1", "Network.responseReceived", map[string]any{
		"requestId": "R1", "response": map[string]any{"status": 200, "mimeType": "text/plain"},
	})
	send("R2", "https://blocked.example/b")
	f.emit("sess-T1", "Network.loadingFailed", map[string]any{
		"requestId": "R2", "errorText": "net::ERR_BLOCKED_BY_CLIENT",
	})
	waitUntil(t, "two requests", func() bool {
		m.col.mu.Lock()
		defer m.col.mu.Unlock()
		return len(m.col.netReqs) == 2 && m.col.netReqs[1].Failed != ""
	})

	out, err := callTool(t, m.listNetworkRequests, `{}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	res := out.(map[string]any)
	if res["total"] != 2 || res["lastReqId"].(int) != 2 {
		t.Fatalf("listing = %v", res)
	}

	out, _ = callTool(t, m.listNetworkRequests, `{"sinceReqId":1}`)
	res = out.(map[string]any)
	reqs := res["requests"].([]*netRequest)
	if res["total"] != 1 || reqs[0].ID != 2 {
		t.Errorf("since-cursor listing = %v", res)
	}

	// failedOnly surfaces exactly the blocked request.
	out, _ = callTool(t, m.listNetworkRequests, `{"failedOnly":true}`)
	res = out.(map[string]any)
	reqs = res["requests"].([]*netRequest)
	if res["total"] != 1 || reqs[0].Failed == "" {
		t.Errorf("failedOnly listing = %v", res)
	}
}
