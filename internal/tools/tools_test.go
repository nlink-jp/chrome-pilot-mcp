package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nlink-jp/chrome-pilot-mcp/internal/browser"
	"github.com/nlink-jp/chrome-pilot-mcp/internal/cdp"
	"github.com/nlink-jp/chrome-pilot-mcp/internal/mcpserver"
	"github.com/nlink-jp/chrome-pilot-mcp/internal/toolerr"
)

// ---- fake CDP endpoint ----

// fakeChrome simulates the browser side of CDP: it tracks page targets and
// answers the methods the tools use. Tests can override per-method handlers.
type fakeChrome struct {
	t *testing.T

	mu       sync.Mutex
	queue    chan []byte
	closed   bool
	pages    []fakePage // ordered
	sessions map[string]string
	// overrides, keyed by method; return (result, cdpErrMsg)
	overrides map[string]func(sessionID string, params map[string]any) (any, string)
	// calls records every method invocation for assertions
	calls []callRec
}

type callRec struct {
	method    string
	sessionID string
	params    map[string]any
}

type fakePage struct {
	targetID string
	url      string
	title    string
}

func newFakeChrome(t *testing.T, initialPages ...string) *fakeChrome {
	f := &fakeChrome{
		t:         t,
		queue:     make(chan []byte, 256),
		sessions:  make(map[string]string),
		overrides: make(map[string]func(string, map[string]any) (any, string)),
	}
	for i, url := range initialPages {
		f.pages = append(f.pages, fakePage{targetID: fmt.Sprintf("T%d", i+1), url: url, title: "page " + url})
	}
	return f
}

func (f *fakeChrome) ReadMessage() ([]byte, error) {
	data, ok := <-f.queue
	if !ok {
		return nil, io.EOF
	}
	return data, nil
}

func (f *fakeChrome) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		close(f.queue)
	}
	return nil
}

func (f *fakeChrome) emit(sessionID, method string, params any) {
	pb, _ := json.Marshal(params)
	frame, _ := json.Marshal(map[string]any{"method": method, "params": json.RawMessage(pb), "sessionId": sessionID})
	f.queue <- frame
}

func (f *fakeChrome) WriteMessage(data []byte) error {
	var req struct {
		ID        int64          `json:"id"`
		Method    string         `json:"method"`
		Params    map[string]any `json:"params"`
		SessionID string         `json:"sessionId"`
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return err
	}
	f.mu.Lock()
	f.calls = append(f.calls, callRec{method: req.Method, sessionID: req.SessionID, params: req.Params})
	override := f.overrides[req.Method]
	f.mu.Unlock()

	var result any
	var errMsg string
	if override != nil {
		result, errMsg = override(req.SessionID, req.Params)
	} else {
		result, errMsg = f.builtin(req.Method, req.SessionID, req.Params)
	}

	var frame []byte
	if errMsg != "" {
		frame, _ = json.Marshal(map[string]any{"id": req.ID, "error": map[string]any{"code": -32000, "message": errMsg}})
	} else {
		rb, _ := json.Marshal(result)
		frame, _ = json.Marshal(map[string]any{"id": req.ID, "result": json.RawMessage(rb)})
	}
	f.queue <- frame
	return nil
}

func (f *fakeChrome) builtin(method, sessionID string, params map[string]any) (any, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch method {
	case "Target.getTargets":
		var infos []map[string]any
		for _, p := range f.pages {
			infos = append(infos, map[string]any{
				"targetId": p.targetID, "type": "page", "url": p.url, "title": p.title,
			})
		}
		return map[string]any{"targetInfos": infos}, ""
	case "Target.createTarget":
		id := fmt.Sprintf("T%d", len(f.pages)+1)
		f.pages = append(f.pages, fakePage{targetID: id, url: params["url"].(string), title: ""})
		return map[string]any{"targetId": id}, ""
	case "Target.attachToTarget":
		tid := params["targetId"].(string)
		sess := "sess-" + tid
		f.sessions[tid] = sess
		return map[string]any{"sessionId": sess}, ""
	case "Target.closeTarget":
		tid := params["targetId"].(string)
		for i, p := range f.pages {
			if p.targetID == tid {
				f.pages = append(f.pages[:i], f.pages[i+1:]...)
				break
			}
		}
		return map[string]any{"success": true}, ""
	case "Page.enable", "Accessibility.enable", "Runtime.enable", "Network.enable",
		"Emulation.setDeviceMetricsOverride", "Emulation.clearDeviceMetricsOverride",
		"Emulation.setEmulatedMedia", "Emulation.setCPUThrottlingRate",
		"Emulation.setGeolocationOverride", "Emulation.setUserAgentOverride",
		"Emulation.setTouchEmulationEnabled",
		"Network.emulateNetworkConditions", "Network.setExtraHTTPHeaders",
		"Input.dispatchMouseEvent", "Input.dispatchKeyEvent", "Input.insertText",
		"DOM.scrollIntoViewIfNeeded", "DOM.setFileInputFiles",
		"Page.handleJavaScriptDialog", "Page.startScreencast", "Page.stopScreencast",
		"Page.screencastFrameAck":
		return map[string]any{}, ""
	case "DOM.getBoxModel":
		// A 20x20 content box centered on (100, 50).
		return map[string]any{"model": map[string]any{
			"content": []float64{90, 40, 110, 40, 110, 60, 90, 60},
		}}, ""
	case "DOM.resolveNode":
		return map[string]any{"object": map[string]any{"objectId": "obj-1"}}, ""
	case "Runtime.callFunctionOn":
		return map[string]any{"result": map[string]any{"type": "undefined"}}, ""
	case "Network.getResponseBody":
		return map[string]any{"body": "", "base64Encoded": false}, ""
	case "Accessibility.getFullAXTree":
		// Default tree: root → button + textbox (used by input tests).
		return map[string]any{"nodes": []map[string]any{
			{"nodeId": "1", "role": map[string]any{"value": "RootWebArea"}, "name": map[string]any{"value": "Test"},
				"childIds": []string{"2", "3"}, "backendDOMNodeId": 100},
			{"nodeId": "2", "role": map[string]any{"value": "button"}, "name": map[string]any{"value": "Go"},
				"childIds": []string{}, "backendDOMNodeId": 101},
			{"nodeId": "3", "role": map[string]any{"value": "textbox"}, "name": map[string]any{"value": "Name"},
				"childIds": []string{}, "backendDOMNodeId": 102},
		}}, ""
	case "Page.navigate":
		url := params["url"].(string)
		for i := range f.pages {
			if f.sessions[f.pages[i].targetID] == sessionID {
				f.pages[i].url = url
			}
		}
		// Fire the load event after responding (queued behind the response).
		go f.emit(sessionID, "Page.loadEventFired", map[string]any{"timestamp": 1})
		return map[string]any{"frameId": "F1", "loaderId": "L1"}, ""
	default:
		f.t.Errorf("fakeChrome: unhandled method %s", method)
		return nil, "unhandled method " + method
	}
}

func (f *fakeChrome) callCount(method string) int {
	return len(f.callsOf(method))
}

func (f *fakeChrome) callsOf(method string) []callRec {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []callRec
	for _, c := range f.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

// newTestManager wires a Manager to a fakeChrome.
func newTestManager(t *testing.T, cfg Config, f *fakeChrome) *Manager {
	m := newManagerWithConnect(cfg, func(ctx context.Context) (*cdp.Client, *browser.Browser, error) {
		return cdp.New(f), nil, nil
	})
	t.Cleanup(m.Shutdown)
	return m
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// callTool goes through wrap() so error mapping is exercised too.
func callTool(t *testing.T, f toolFunc, args string) (any, error) {
	t.Helper()
	return wrap(f)(ctxT(t), json.RawMessage(args))
}

// ---- tests ----

func TestListPages(t *testing.T) {
	f := newFakeChrome(t, "about:blank", "https://example.com/")
	m := newTestManager(t, Config{}, f)

	out, err := callTool(t, m.listPages, `{}`)
	if err != nil {
		t.Fatalf("list_pages: %v", err)
	}
	pages := out.(map[string]any)["pages"].([]pageEntry)
	if len(pages) != 2 {
		t.Fatalf("got %d pages, want 2", len(pages))
	}
	if !pages[0].Selected || pages[1].Selected {
		t.Errorf("first page should be auto-selected: %+v", pages)
	}
	if pages[1].URL != "https://example.com/" {
		t.Errorf("page 1 url = %q", pages[1].URL)
	}
}

func TestNewPageNavigatesAndSelects(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)

	out, err := callTool(t, m.newPage, `{"url":"https://example.com/"}`)
	if err != nil {
		t.Fatalf("new_page: %v", err)
	}
	res := out.(map[string]any)
	if res["index"] != 1 {
		t.Errorf("new page index = %v, want 1", res["index"])
	}

	listOut, err := callTool(t, m.listPages, `{}`)
	if err != nil {
		t.Fatalf("list_pages: %v", err)
	}
	pages := listOut.(map[string]any)["pages"].([]pageEntry)
	if len(pages) != 2 || !pages[1].Selected {
		t.Errorf("new page should be selected: %+v", pages)
	}
	if pages[1].URL != "https://example.com/" {
		t.Errorf("navigated url = %q", pages[1].URL)
	}
}

func TestNavigateErrorText(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	f.overrides["Page.navigate"] = func(sessionID string, params map[string]any) (any, string) {
		return map[string]any{"frameId": "F1", "errorText": "net::ERR_NAME_NOT_RESOLVED"}, ""
	}
	m := newTestManager(t, Config{}, f)

	_, err := callTool(t, m.navigatePage, `{"url":"https://nope.invalid/"}`)
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Code != toolerr.CodeCDPError {
		t.Fatalf("want cdp_error, got %v", err)
	}
	if !strings.Contains(te.Message, "ERR_NAME_NOT_RESOLVED") {
		t.Errorf("message = %q", te.Message)
	}
}

func TestNavigateTimeout(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	f.overrides["Page.navigate"] = func(sessionID string, params map[string]any) (any, string) {
		return map[string]any{"frameId": "F1"}, "" // never fires loadEventFired
	}
	m := newTestManager(t, Config{}, f)

	_, err := callTool(t, m.navigatePage, `{"url":"https://slow.example/","timeout":50}`)
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Code != toolerr.CodeTimeout {
		t.Fatalf("want timeout error, got %v", err)
	}
}

func TestSelectAndClosePage(t *testing.T) {
	f := newFakeChrome(t, "about:blank", "https://a.example/", "https://b.example/")
	m := newTestManager(t, Config{}, f)

	if _, err := callTool(t, m.selectPage, `{"pageIdx":2}`); err != nil {
		t.Fatalf("select_page: %v", err)
	}
	out, err := callTool(t, m.closePage, `{"pageIdx":0}`)
	if err != nil {
		t.Fatalf("close_page: %v", err)
	}
	if out.(map[string]any)["remaining"] != 2 {
		t.Errorf("remaining = %v", out.(map[string]any)["remaining"])
	}
	// Selected page (b.example) must survive the close of page 0.
	listOut, _ := callTool(t, m.listPages, `{}`)
	pages := listOut.(map[string]any)["pages"].([]pageEntry)
	if !pages[1].Selected || pages[1].URL != "https://b.example/" {
		t.Errorf("selection lost after close: %+v", pages)
	}

	if _, err := callTool(t, m.selectPage, `{"pageIdx":9}`); err == nil {
		t.Errorf("select_page out of range should fail")
	}
	if _, err := callTool(t, m.selectPage, `{}`); err == nil {
		t.Errorf("select_page without pageIdx should fail")
	}
}

func TestWaitForText(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	evals := 0
	f.overrides["Runtime.evaluate"] = func(sessionID string, params map[string]any) (any, string) {
		evals++
		return map[string]any{"result": map[string]any{"type": "boolean", "value": evals >= 3}}, ""
	}
	m := newTestManager(t, Config{}, f)

	out, err := callTool(t, m.waitFor, `{"text":"done"}`)
	if err != nil {
		t.Fatalf("wait_for: %v", err)
	}
	if out.(map[string]any)["found"] != true {
		t.Errorf("found = %v", out)
	}
	if evals < 3 {
		t.Errorf("expected at least 3 polls, got %d", evals)
	}
}

func TestWaitForTimeout(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	f.overrides["Runtime.evaluate"] = func(sessionID string, params map[string]any) (any, string) {
		return map[string]any{"result": map[string]any{"type": "boolean", "value": false}}, ""
	}
	m := newTestManager(t, Config{}, f)

	_, err := callTool(t, m.waitFor, `{"text":"never","timeout":50}`)
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Code != toolerr.CodeTimeout {
		t.Fatalf("want timeout, got %v", err)
	}
}

func TestEvaluateScript(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	f.overrides["Runtime.evaluate"] = func(sessionID string, params map[string]any) (any, string) {
		expr := params["expression"].(string)
		if !strings.HasPrefix(expr, "(") || !strings.HasSuffix(expr, ")()") {
			t.Errorf("expression not wrapped as call: %q", expr)
		}
		if params["awaitPromise"] != true || params["returnByValue"] != true {
			t.Errorf("missing awaitPromise/returnByValue: %v", params)
		}
		return map[string]any{"result": map[string]any{"type": "number", "value": 42}}, ""
	}
	m := newTestManager(t, Config{}, f)

	out, err := callTool(t, m.evaluateScript, `{"function":"() => 42"}`)
	if err != nil {
		t.Fatalf("evaluate_script: %v", err)
	}
	if string(out.(map[string]any)["value"].(json.RawMessage)) != "42" {
		t.Errorf("value = %s", out.(map[string]any)["value"])
	}
}

func TestEvaluateScriptException(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	f.overrides["Runtime.evaluate"] = func(sessionID string, params map[string]any) (any, string) {
		return map[string]any{
			"result":           map[string]any{"type": "object"},
			"exceptionDetails": map[string]any{"text": "Uncaught", "exception": map[string]any{"description": "Error: boom"}},
		}, ""
	}
	m := newTestManager(t, Config{}, f)

	_, err := callTool(t, m.evaluateScript, `{"function":"() => { throw new Error('boom') }"}`)
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Code != toolerr.CodeScriptFailed {
		t.Fatalf("want script_failed, got %v", err)
	}
	if !strings.Contains(te.Message, "boom") {
		t.Errorf("message = %q", te.Message)
	}
}

func TestTakeScreenshot(t *testing.T) {
	imgBytes := []byte("PNG-PAYLOAD")
	f := newFakeChrome(t, "https://example.com/")
	f.overrides["Page.captureScreenshot"] = func(sessionID string, params map[string]any) (any, string) {
		if params["format"] != "png" {
			t.Errorf("format = %v", params["format"])
		}
		return map[string]any{"data": base64.StdEncoding.EncodeToString(imgBytes)}, ""
	}
	ws := t.TempDir()
	m := newTestManager(t, Config{WorkspaceRoot: ws}, f)

	out, err := callTool(t, m.takeScreenshot, `{}`)
	if err != nil {
		t.Fatalf("take_screenshot: %v", err)
	}
	raw := out.(mcpserver.RawResult)
	if len(raw.Content) != 2 {
		t.Fatalf("want text+image blocks, got %d", len(raw.Content))
	}
	if raw.Content[1].Type != "image" || raw.Content[1].MimeType != "image/png" {
		t.Errorf("image block = %+v", raw.Content[1])
	}

	var meta struct {
		Path  string `json:"path"`
		Bytes int    `json:"bytes"`
	}
	if err := json.Unmarshal([]byte(raw.Content[0].Text), &meta); err != nil {
		t.Fatalf("meta not JSON: %v", err)
	}
	if !strings.HasPrefix(meta.Path, ws) {
		t.Errorf("path %q not under workspace %q", meta.Path, ws)
	}
	got, err := os.ReadFile(meta.Path)
	if err != nil || string(got) != string(imgBytes) {
		t.Errorf("file contents mismatch: %v %q", err, got)
	}
}

func TestTakeScreenshotBadFormat(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{WorkspaceRoot: t.TempDir()}, f)

	_, err := callTool(t, m.takeScreenshot, `{"format":"webp"}`)
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Code != toolerr.CodeInvalidArguments {
		t.Fatalf("want invalid_arguments, got %v", err)
	}
}

func TestTakeSnapshot(t *testing.T) {
	f := newFakeChrome(t, "https://example.com/")
	f.overrides["Accessibility.getFullAXTree"] = func(sessionID string, params map[string]any) (any, string) {
		return map[string]any{"nodes": []map[string]any{
			{"nodeId": "1", "role": map[string]any{"value": "RootWebArea"}, "name": map[string]any{"value": "Example"},
				"childIds": []string{"2", "3"}, "backendDOMNodeId": 100},
			{"nodeId": "2", "role": map[string]any{"value": "heading"}, "name": map[string]any{"value": "Hello"},
				"childIds": []string{}, "backendDOMNodeId": 101},
			{"nodeId": "3", "ignored": true, "role": map[string]any{"value": "generic"},
				"childIds": []string{"4"}, "backendDOMNodeId": 102},
			{"nodeId": "4", "role": map[string]any{"value": "StaticText"}, "name": map[string]any{"value": "World"},
				"childIds": []string{}, "backendDOMNodeId": 103},
		}}, ""
	}
	m := newTestManager(t, Config{}, f)

	out, err := callTool(t, m.takeSnapshot, `{}`)
	if err != nil {
		t.Fatalf("take_snapshot: %v", err)
	}
	text := out.(mcpserver.RawResult).Content[0].Text

	if !strings.Contains(text, "url: https://example.com/") {
		t.Errorf("missing url header:\n%s", text)
	}
	if !strings.Contains(text, `uid=1_1 RootWebArea "Example"`) {
		t.Errorf("missing root line:\n%s", text)
	}
	if !strings.Contains(text, `  uid=1_2 heading "Hello"`) {
		t.Errorf("missing indented heading:\n%s", text)
	}
	// Ignored node is skipped but its child is promoted (same depth).
	if !strings.Contains(text, `  uid=1_3 StaticText "World"`) {
		t.Errorf("ignored node's child not promoted:\n%s", text)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if got := m.uids["1_2"].backendNodeID; got != 101 {
		t.Errorf("uid 1_2 backend node = %d, want 101", got)
	}
}

func TestStrictArgsRejected(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)

	_, err := callTool(t, m.navigatePage, `{"url":"https://x.example/","typo":true}`)
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Code != toolerr.CodeInvalidArguments {
		t.Fatalf("want invalid_arguments for unknown field, got %v", err)
	}
}

// TestLazyConnect: creating the manager must not connect; the first tool
// call does.
func TestLazyConnect(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	connected := 0
	m := newManagerWithConnect(Config{}, func(ctx context.Context) (*cdp.Client, *browser.Browser, error) {
		connected++
		return cdp.New(f), nil, nil
	})
	t.Cleanup(m.Shutdown)

	if connected != 0 {
		t.Fatalf("manager connected eagerly")
	}
	if _, err := callTool(t, m.listPages, `{}`); err != nil {
		t.Fatalf("list_pages: %v", err)
	}
	if _, err := callTool(t, m.listPages, `{}`); err != nil {
		t.Fatalf("list_pages: %v", err)
	}
	if connected != 1 {
		t.Errorf("connected %d times, want 1", connected)
	}
	if f.callCount("Target.getTargets") != 2 {
		t.Errorf("getTargets calls = %d, want 2", f.callCount("Target.getTargets"))
	}
}
