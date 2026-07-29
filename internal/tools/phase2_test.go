package tools

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/chrome-pilot-mcp/internal/mcpserver"
	"github.com/nlink-jp/chrome-pilot-mcp/internal/toolerr"
)

// snapshotFirst takes a snapshot so uids exist (default fake tree:
// 1_1 root / 1_2 button / 1_3 textbox on the first snapshot).
func snapshotFirst(t *testing.T, m *Manager) {
	t.Helper()
	if _, err := callTool(t, m.takeSnapshot, `{}`); err != nil {
		t.Fatalf("take_snapshot: %v", err)
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ---- input tools ----

func TestClickElement(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	snapshotFirst(t, m)

	out, err := callTool(t, m.click, `{"uid":"1_2"}`)
	if err != nil {
		t.Fatalf("click: %v", err)
	}
	if !strings.Contains(out.(mcpserver.RawResult).Content[0].Text, `"clicked":"1_2"`) {
		t.Errorf("result = %+v", out)
	}

	events := f.callsOf("Input.dispatchMouseEvent")
	if len(events) != 3 { // move, press, release
		t.Fatalf("got %d mouse events, want 3", len(events))
	}
	press := events[1].params
	if press["type"] != "mousePressed" || press["x"] != 100.0 || press["y"] != 50.0 || press["clickCount"] != 1.0 {
		t.Errorf("press event = %v", press)
	}
}

func TestDoubleClick(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	snapshotFirst(t, m)

	if _, err := callTool(t, m.click, `{"uid":"1_2","dblClick":true}`); err != nil {
		t.Fatalf("dblclick: %v", err)
	}
	events := f.callsOf("Input.dispatchMouseEvent")
	if len(events) != 5 { // move + 2×(press+release)
		t.Fatalf("got %d mouse events, want 5", len(events))
	}
	if events[3].params["clickCount"] != 2.0 {
		t.Errorf("second press clickCount = %v", events[3].params["clickCount"])
	}
}

func TestClickUnknownUID(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	snapshotFirst(t, m)

	_, err := callTool(t, m.click, `{"uid":"9_9"}`)
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Code != toolerr.CodeElementNotFound {
		t.Fatalf("want element_not_found, got %v", err)
	}
}

func TestClickIncludeSnapshot(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	snapshotFirst(t, m)

	out, err := callTool(t, m.click, `{"uid":"1_2","includeSnapshot":true}`)
	if err != nil {
		t.Fatalf("click: %v", err)
	}
	blocks := out.(mcpserver.RawResult).Content
	if len(blocks) != 2 || !strings.Contains(blocks[1].Text, "Page snapshot") {
		t.Errorf("expected result + snapshot blocks, got %+v", blocks)
	}
	// The fresh snapshot re-seeded uids with the next sequence.
	if _, err := m.resolveUID("2_2"); err != nil {
		t.Errorf("fresh snapshot uid 2_2 should resolve: %v", err)
	}
}

func TestClickAt(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)

	if _, err := callTool(t, m.clickAt, `{"x":10,"y":20}`); err != nil {
		t.Fatalf("click_at: %v", err)
	}
	events := f.callsOf("Input.dispatchMouseEvent")
	if len(events) != 3 || events[1].params["x"] != 10.0 || events[1].params["y"] != 20.0 {
		t.Errorf("events = %+v", events)
	}
	if _, err := callTool(t, m.clickAt, `{"x":10}`); err == nil {
		t.Errorf("click_at without y should fail")
	}
}

func TestHoverAndDrag(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	snapshotFirst(t, m)

	if _, err := callTool(t, m.hover, `{"uid":"1_2"}`); err != nil {
		t.Fatalf("hover: %v", err)
	}
	if n := f.callCount("Input.dispatchMouseEvent"); n != 1 {
		t.Errorf("hover mouse events = %d, want 1", n)
	}

	if _, err := callTool(t, m.drag, `{"from_uid":"1_2","to_uid":"1_3"}`); err != nil {
		t.Fatalf("drag: %v", err)
	}
	events := f.callsOf("Input.dispatchMouseEvent")
	last := events[len(events)-1].params
	if last["type"] != "mouseReleased" {
		t.Errorf("drag should end with mouseReleased, got %v", last["type"])
	}
}

func TestFill(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	snapshotFirst(t, m)

	if _, err := callTool(t, m.fill, `{"uid":"1_3","value":"magi"}`); err != nil {
		t.Fatalf("fill: %v", err)
	}
	calls := f.callsOf("Runtime.callFunctionOn")
	if len(calls) != 1 {
		t.Fatalf("callFunctionOn calls = %d", len(calls))
	}
	args := calls[0].params["arguments"].([]any)
	if args[0].(map[string]any)["value"] != "magi" {
		t.Errorf("fill value = %v", args[0])
	}
	if calls[0].params["objectId"] != "obj-1" {
		t.Errorf("objectId = %v", calls[0].params["objectId"])
	}
}

func TestFillNotFillable(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	f.overrides["Runtime.callFunctionOn"] = func(sessionID string, params map[string]any) (any, string) {
		return map[string]any{"exceptionDetails": map[string]any{
			"text": "Uncaught", "exception": map[string]any{"description": "Error: element <div> is not fillable"},
		}}, ""
	}
	m := newTestManager(t, Config{}, f)
	snapshotFirst(t, m)

	_, err := callTool(t, m.fill, `{"uid":"1_2","value":"x"}`)
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Code != toolerr.CodeScriptFailed {
		t.Fatalf("want script_failed, got %v", err)
	}
}

func TestFillForm(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	snapshotFirst(t, m)

	out, err := callTool(t, m.fillForm, `{"elements":[{"uid":"1_2","value":"a"},{"uid":"1_3","value":"b"}]}`)
	if err != nil {
		t.Fatalf("fill_form: %v", err)
	}
	if !strings.Contains(out.(mcpserver.RawResult).Content[0].Text, `"filled":2`) {
		t.Errorf("result = %+v", out)
	}
	if n := f.callCount("Runtime.callFunctionOn"); n != 2 {
		t.Errorf("callFunctionOn calls = %d, want 2", n)
	}
}

func TestTypeTextWithSubmitKey(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)

	if _, err := callTool(t, m.typeText, `{"text":"hello","submitKey":"Enter"}`); err != nil {
		t.Fatalf("type_text: %v", err)
	}
	ins := f.callsOf("Input.insertText")
	if len(ins) != 1 || ins[0].params["text"] != "hello" {
		t.Errorf("insertText = %+v", ins)
	}
	keys := f.callsOf("Input.dispatchKeyEvent")
	if len(keys) != 2 {
		t.Fatalf("key events = %d, want 2 (down+up)", len(keys))
	}
	if keys[0].params["type"] != "keyDown" || keys[0].params["text"] != "\r" {
		t.Errorf("keyDown = %v", keys[0].params)
	}
}

func TestParseKeyCombo(t *testing.T) {
	tests := []struct {
		combo   string
		mods    int
		key     string
		keyCode int
	}{
		{"Enter", 0, "Enter", 13},
		{"a", 0, "a", 65},
		{"Control+A", 2, "A", 65},
		{"Control+Shift+R", 10, "R", 82},
		{"Control++", 2, "+", int('+')},
		{"Meta+c", 4, "c", 67},
		{"+", 0, "+", int('+')},
	}
	for _, tt := range tests {
		mods, def, err := parseKeyCombo(tt.combo)
		if err != nil {
			t.Errorf("%q: %v", tt.combo, err)
			continue
		}
		if mods != tt.mods || def.key != tt.key || def.keyCode != tt.keyCode {
			t.Errorf("%q → mods=%d key=%q code=%d, want mods=%d key=%q code=%d",
				tt.combo, mods, def.key, def.keyCode, tt.mods, tt.key, tt.keyCode)
		}
	}
	for _, bad := range []string{"Hyper+A", "Control+Foo", ""} {
		if _, _, err := parseKeyCombo(bad); err == nil {
			t.Errorf("parseKeyCombo(%q) should fail", bad)
		}
	}
}

func TestPressKeyModifierSuppressesText(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)

	if _, err := callTool(t, m.pressKey, `{"key":"Control+A"}`); err != nil {
		t.Fatalf("press_key: %v", err)
	}
	keys := f.callsOf("Input.dispatchKeyEvent")
	if keys[0].params["type"] != "rawKeyDown" {
		t.Errorf("Control+A should be rawKeyDown (no text), got %v", keys[0].params)
	}
	if keys[0].params["modifiers"] != 2.0 {
		t.Errorf("modifiers = %v, want 2", keys[0].params["modifiers"])
	}
	// Synthetic CDP keys bypass browser shortcut handling; select-all only
	// works when the edit command is sent explicitly.
	cmds, _ := keys[0].params["commands"].([]any)
	if len(cmds) != 1 || cmds[0] != "selectAll" {
		t.Errorf("Control+A commands = %v, want [selectAll]", keys[0].params["commands"])
	}
}

func TestUploadFile(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	snapshotFirst(t, m)

	path := filepath.Join(t.TempDir(), "data.csv")
	if err := os.WriteFile(path, []byte("a,b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := callTool(t, m.uploadFile, fmt.Sprintf(`{"uid":"1_3","filePath":%q}`, path)); err != nil {
		t.Fatalf("upload_file: %v", err)
	}
	calls := f.callsOf("DOM.setFileInputFiles")
	if len(calls) != 1 {
		t.Fatalf("setFileInputFiles calls = %d", len(calls))
	}
	files := calls[0].params["files"].([]any)
	if files[0] != path {
		t.Errorf("files = %v", files)
	}

	_, err := callTool(t, m.uploadFile, `{"uid":"1_3","filePath":"/no/such/file.bin"}`)
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Code != toolerr.CodeInvalidArguments {
		t.Errorf("missing file should be invalid_arguments, got %v", err)
	}
}

func TestHandleDialog(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)

	// No attached page yet → dialog_not_open.
	_, err := callTool(t, m.handleDialog, `{"action":"accept"}`)
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Code != toolerr.CodeDialogNotOpen {
		t.Fatalf("want dialog_not_open, got %v", err)
	}

	snapshotFirst(t, m) // attaches sess-T1
	f.emit("sess-T1", "Page.javascriptDialogOpening", map[string]any{"type": "confirm", "message": "sure?"})
	waitUntil(t, "dialog tracked", func() bool {
		m.col.mu.Lock()
		defer m.col.mu.Unlock()
		return m.col.dialogs["sess-T1"] != nil
	})

	out, err := callTool(t, m.handleDialog, `{"action":"accept","promptText":"yes"}`)
	if err != nil {
		t.Fatalf("handle_dialog: %v", err)
	}
	res := out.(map[string]any)
	if res["dialogType"] != "confirm" || res["message"] != "sure?" {
		t.Errorf("result = %v", res)
	}
	calls := f.callsOf("Page.handleJavaScriptDialog")
	if len(calls) != 1 || calls[0].params["accept"] != true || calls[0].params["promptText"] != "yes" {
		t.Errorf("handleJavaScriptDialog = %+v", calls)
	}
	m.col.mu.Lock()
	cleared := m.col.dialogs["sess-T1"] == nil
	m.col.mu.Unlock()
	if !cleared {
		t.Errorf("dialog state not cleared after handling")
	}
}

// ---- console / network ----

func TestConsoleMessages(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	snapshotFirst(t, m) // attach + Runtime.enable

	f.emit("sess-T1", "Runtime.consoleAPICalled", map[string]any{
		"type": "log", "timestamp": 1.0,
		"args": []map[string]any{{"type": "string", "value": "hello"}, {"type": "number", "value": 42}},
	})
	f.emit("sess-T1", "Runtime.exceptionThrown", map[string]any{
		"timestamp": 2.0,
		"exceptionDetails": map[string]any{
			"text": "Uncaught", "exception": map[string]any{"type": "object", "description": "Error: kaboom"},
		},
	})
	waitUntil(t, "console messages", func() bool {
		m.col.mu.Lock()
		defer m.col.mu.Unlock()
		return len(m.col.consoleMsgs) == 2
	})

	out, err := callTool(t, m.listConsoleMessages, `{}`)
	if err != nil {
		t.Fatalf("list_console_messages: %v", err)
	}
	res := out.(map[string]any)
	msgs := res["messages"].([]*consoleMsg)
	if res["total"] != 2 || len(msgs) != 2 {
		t.Fatalf("total=%v len=%d", res["total"], len(msgs))
	}
	if msgs[0].Text != "hello 42" || msgs[0].Type != "log" {
		t.Errorf("msg[0] = %+v", msgs[0])
	}
	if msgs[1].Type != "error" || !strings.Contains(msgs[1].Text, "kaboom") {
		t.Errorf("msg[1] = %+v", msgs[1])
	}

	// Filter by type.
	out, _ = callTool(t, m.listConsoleMessages, `{"types":["error"]}`)
	if out.(map[string]any)["total"] != 1 {
		t.Errorf("error filter total = %v", out.(map[string]any)["total"])
	}

	// Detail fetch.
	out, err = callTool(t, m.getConsoleMessage, fmt.Sprintf(`{"msgid":%d}`, msgs[0].ID))
	if err != nil {
		t.Fatalf("get_console_message: %v", err)
	}
	if out.(map[string]any)["text"] != "hello 42" {
		t.Errorf("detail = %v", out)
	}
	if _, err := callTool(t, m.getConsoleMessage, `{"msgid":999}`); err == nil {
		t.Errorf("unknown msgid should fail")
	}
}

func TestNetworkRequests(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	snapshotFirst(t, m) // attach + Network.enable

	f.emit("sess-T1", "Network.requestWillBeSent", map[string]any{
		"requestId": "R1", "type": "Fetch",
		"request": map[string]any{"url": "https://api.example/data", "method": "GET",
			"headers": map[string]any{"Accept": "application/json"}},
	})
	f.emit("sess-T1", "Network.responseReceived", map[string]any{
		"requestId": "R1",
		"response":  map[string]any{"status": 200, "mimeType": "application/json", "headers": map[string]any{"X-Rate": "10"}},
	})
	f.emit("sess-T1", "Network.loadingFinished", map[string]any{"requestId": "R1", "encodedDataLength": 512})
	f.emit("sess-T1", "Network.requestWillBeSent", map[string]any{
		"requestId": "R2", "type": "Image",
		"request": map[string]any{"url": "https://cdn.example/x.png", "method": "GET"},
	})
	waitUntil(t, "network records", func() bool {
		m.col.mu.Lock()
		defer m.col.mu.Unlock()
		return len(m.col.netReqs) == 2 && m.col.netReqs[0].Finished
	})

	out, err := callTool(t, m.listNetworkRequests, `{}`)
	if err != nil {
		t.Fatalf("list_network_requests: %v", err)
	}
	res := out.(map[string]any)
	reqs := res["requests"].([]*netRequest)
	if res["total"] != 2 || reqs[0].Status != 200 || reqs[0].Size != 512 {
		t.Errorf("reqs = %+v", reqs)
	}

	out, _ = callTool(t, m.listNetworkRequests, `{"resourceTypes":["Fetch"]}`)
	if out.(map[string]any)["total"] != 1 {
		t.Errorf("Fetch filter total = %v", out.(map[string]any)["total"])
	}

	f.overrides["Network.getResponseBody"] = func(sessionID string, params map[string]any) (any, string) {
		if params["requestId"] != "R1" {
			t.Errorf("requestId = %v", params["requestId"])
		}
		return map[string]any{"body": `{"ok":true}`, "base64Encoded": false}, ""
	}
	out, err = callTool(t, m.getNetworkRequest, fmt.Sprintf(`{"reqid":%d}`, reqs[0].ID))
	if err != nil {
		t.Fatalf("get_network_request: %v", err)
	}
	detail := out.(map[string]any)
	if detail["body"] != `{"ok":true}` || detail["status"] != 200 {
		t.Errorf("detail = %v", detail)
	}
}

// ---- emulation ----

func TestResizePage(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)

	if _, err := callTool(t, m.resizePage, `{"width":800,"height":600}`); err != nil {
		t.Fatalf("resize_page: %v", err)
	}
	calls := f.callsOf("Emulation.setDeviceMetricsOverride")
	if len(calls) != 1 || calls[0].params["width"] != 800.0 || calls[0].params["height"] != 600.0 {
		t.Errorf("override = %+v", calls)
	}
	if _, err := callTool(t, m.resizePage, `{"width":800}`); err == nil {
		t.Errorf("missing height should fail")
	}
}

func TestEmulate(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)

	_, err := callTool(t, m.emulate,
		`{"colorScheme":"dark","networkConditions":"Slow 3G","cpuThrottlingRate":4,"viewport":"390x844x3,mobile,touch","geolocation":"35.68,139.76"}`)
	if err != nil {
		t.Fatalf("emulate: %v", err)
	}

	media := f.callsOf("Emulation.setEmulatedMedia")
	features := media[0].params["features"].([]any)[0].(map[string]any)
	if features["value"] != "dark" {
		t.Errorf("colorScheme feature = %v", features)
	}
	net := f.callsOf("Network.emulateNetworkConditions")
	if net[0].params["latency"] != 2000.0 {
		t.Errorf("network latency = %v", net[0].params["latency"])
	}
	cpu := f.callsOf("Emulation.setCPUThrottlingRate")
	if cpu[0].params["rate"] != 4.0 {
		t.Errorf("cpu rate = %v", cpu[0].params["rate"])
	}
	vp := f.callsOf("Emulation.setDeviceMetricsOverride")
	p := vp[0].params
	if p["width"] != 390.0 || p["deviceScaleFactor"] != 3.0 || p["mobile"] != true {
		t.Errorf("viewport override = %v", p)
	}
	touch := f.callsOf("Emulation.setTouchEmulationEnabled")
	if touch[0].params["enabled"] != true {
		t.Errorf("touch = %v", touch[0].params)
	}
	geo := f.callsOf("Emulation.setGeolocationOverride")
	if geo[0].params["latitude"] != 35.68 {
		t.Errorf("geo = %v", geo[0].params)
	}

	// Omitting viewport clears the override.
	if _, err := callTool(t, m.emulate, `{}`); err != nil {
		t.Fatalf("emulate reset: %v", err)
	}
	if n := f.callCount("Emulation.clearDeviceMetricsOverride"); n != 1 {
		t.Errorf("clearDeviceMetricsOverride calls = %d, want 1", n)
	}

	if _, err := callTool(t, m.emulate, `{"viewport":"bogus"}`); err == nil {
		t.Errorf("bad viewport should fail")
	}
	if _, err := callTool(t, m.emulate, `{"networkConditions":"Warp 9"}`); err == nil {
		t.Errorf("bad networkConditions should fail")
	}
}

// ---- screencast ----

func encodeTestJPEG(t *testing.T, w, h int, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestScreencastGIF(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	ws := t.TempDir()
	m := newTestManager(t, Config{WorkspaceRoot: ws}, f)

	// Stop without start.
	_, err := callTool(t, m.screencastStop, `{}`)
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Code != toolerr.CodeScreencastNotActive {
		t.Fatalf("want screencast_not_active, got %v", err)
	}

	if _, err := callTool(t, m.screencastStart, `{}`); err != nil {
		t.Fatalf("screencast_start: %v", err)
	}
	start := f.callsOf("Page.startScreencast")
	if start[0].params["format"] != "jpeg" || start[0].params["maxWidth"] != 800.0 {
		t.Errorf("startScreencast params = %v", start[0].params)
	}

	// Double start refused.
	if _, err := callTool(t, m.screencastStart, `{}`); err == nil {
		t.Errorf("second screencast_start should fail")
	}

	frame1 := encodeTestJPEG(t, 20, 10, color.RGBA{255, 0, 0, 255})
	frame2 := encodeTestJPEG(t, 20, 10, color.RGBA{0, 0, 255, 255})
	f.emit("sess-T1", "Page.screencastFrame", map[string]any{
		"data": base64.StdEncoding.EncodeToString(frame1), "sessionId": 1,
		"metadata": map[string]any{"timestamp": 100.0},
	})
	f.emit("sess-T1", "Page.screencastFrame", map[string]any{
		"data": base64.StdEncoding.EncodeToString(frame2), "sessionId": 2,
		"metadata": map[string]any{"timestamp": 100.5},
	})
	waitUntil(t, "frames stored", func() bool {
		m.col.mu.Lock()
		defer m.col.mu.Unlock()
		sc := m.col.screencasts["sess-T1"]
		return sc != nil && len(sc.frames) == 2
	})

	out, err := callTool(t, m.screencastStop, `{}`)
	if err != nil {
		t.Fatalf("screencast_stop: %v", err)
	}
	res := out.(map[string]any)
	if res["frames"] != 2 {
		t.Errorf("frames = %v", res["frames"])
	}
	path := res["path"].(string)
	if !strings.HasPrefix(path, ws) || !strings.HasSuffix(path, ".gif") {
		t.Errorf("path = %q", path)
	}

	// The file must be a decodable 2-frame animated GIF with a 50cs delay
	// between the frames (0.5s gap in capture timestamps).
	gf, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer gf.Close()
	g, err := gif.DecodeAll(gf)
	if err != nil {
		t.Fatalf("decode gif: %v", err)
	}
	if len(g.Image) != 2 {
		t.Fatalf("gif frames = %d", len(g.Image))
	}
	if g.Delay[0] != 50 {
		t.Errorf("delay[0] = %d, want 50", g.Delay[0])
	}
	if got := g.Image[0].Bounds(); got.Dx() != 20 || got.Dy() != 10 {
		t.Errorf("frame bounds = %v", got)
	}

	// Frame acks were sent for both frames.
	waitUntil(t, "frame acks", func() bool { return f.callCount("Page.screencastFrameAck") == 2 })
}

// TestScreencastRefitsResizedFrames is the regression for the behavior
// found in real use: resizing the viewport mid-recording dropped every
// later frame, leaving a one-frame GIF.
func TestScreencastRefitsResizedFrames(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	ws := t.TempDir()
	m := newTestManager(t, Config{WorkspaceRoot: ws}, f)

	if _, err := callTool(t, m.screencastStart, `{}`); err != nil {
		t.Fatalf("screencast_start: %v", err)
	}
	// Small frame, then two frames from a larger viewport.
	for i, fr := range [][]byte{
		encodeTestJPEG(t, 20, 10, color.RGBA{255, 0, 0, 255}),
		encodeTestJPEG(t, 40, 30, color.RGBA{0, 255, 0, 255}),
		encodeTestJPEG(t, 40, 30, color.RGBA{0, 0, 255, 255}),
	} {
		f.emit("sess-T1", "Page.screencastFrame", map[string]any{
			"data": base64.StdEncoding.EncodeToString(fr), "sessionId": i + 1,
			"metadata": map[string]any{"timestamp": 100.0 + float64(i)/2},
		})
	}
	waitUntil(t, "frames stored", func() bool {
		m.col.mu.Lock()
		defer m.col.mu.Unlock()
		sc := m.col.screencasts["sess-T1"]
		return sc != nil && len(sc.frames) == 3
	})

	out, err := callTool(t, m.screencastStop, `{}`)
	if err != nil {
		t.Fatalf("screencast_stop: %v", err)
	}
	res := out.(map[string]any)
	if res["frames"] != 3 {
		t.Errorf("frames = %v, want all 3 kept", res["frames"])
	}
	if res["refittedFrames"] != 1 {
		t.Errorf("refittedFrames = %v, want 1 (the small first frame)", res["refittedFrames"])
	}
	if res["width"] != 40 || res["height"] != 30 {
		t.Errorf("canvas = %vx%v, want the largest frame 40x30", res["width"], res["height"])
	}
	if note, _ := res["note"].(string); !strings.Contains(note, "viewport changed") {
		t.Errorf("note should explain the refit: %q", note)
	}

	gf, err := os.Open(res["path"].(string))
	if err != nil {
		t.Fatal(err)
	}
	defer gf.Close()
	g, err := gif.DecodeAll(gf)
	if err != nil {
		t.Fatalf("decode gif: %v", err)
	}
	if len(g.Image) != 3 {
		t.Errorf("gif has %d frames, want 3", len(g.Image))
	}
	for i, img := range g.Image {
		if b := img.Bounds(); b.Dx() != 40 || b.Dy() != 30 {
			t.Errorf("frame %d bounds = %v, want the shared 40x30 canvas", i, b)
		}
	}
}

func TestScreencastMaxFrames(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{WorkspaceRoot: t.TempDir()}, f)

	if _, err := callTool(t, m.screencastStart, `{"maxFrames":2}`); err != nil {
		t.Fatalf("screencast_start: %v", err)
	}
	for i := range 5 {
		f.emit("sess-T1", "Page.screencastFrame", map[string]any{
			"data":      base64.StdEncoding.EncodeToString(encodeTestJPEG(t, 20, 10, color.RGBA{byte(i * 50), 0, 0, 255})),
			"sessionId": i + 1,
			"metadata":  map[string]any{"timestamp": 100.0 + float64(i)},
		})
	}
	waitUntil(t, "frames dropped", func() bool {
		m.col.mu.Lock()
		defer m.col.mu.Unlock()
		sc := m.col.screencasts["sess-T1"]
		return sc != nil && sc.dropped >= 3
	})

	out, err := callTool(t, m.screencastStop, `{}`)
	if err != nil {
		t.Fatalf("screencast_stop: %v", err)
	}
	res := out.(map[string]any)
	if res["frames"] != 2 {
		t.Errorf("frames = %v, want the 2 allowed", res["frames"])
	}
	if res["truncated"] != "maxFrames" {
		t.Errorf("truncated = %v, want maxFrames", res["truncated"])
	}
	if res["droppedFrames"] != 3 {
		t.Errorf("droppedFrames = %v, want 3", res["droppedFrames"])
	}
}

func TestScreencastMaxDuration(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{WorkspaceRoot: t.TempDir()}, f)

	if _, err := callTool(t, m.screencastStart, `{"maxDurationMs":1000}`); err != nil {
		t.Fatalf("screencast_start: %v", err)
	}
	// Timestamps are seconds: 0s, 0.5s (kept) then 2s (past the 1s budget).
	for i, ts := range []float64{100.0, 100.5, 102.0} {
		f.emit("sess-T1", "Page.screencastFrame", map[string]any{
			"data":      base64.StdEncoding.EncodeToString(encodeTestJPEG(t, 20, 10, color.RGBA{0, byte(i * 60), 0, 255})),
			"sessionId": i + 1,
			"metadata":  map[string]any{"timestamp": ts},
		})
	}
	waitUntil(t, "late frame dropped", func() bool {
		m.col.mu.Lock()
		defer m.col.mu.Unlock()
		sc := m.col.screencasts["sess-T1"]
		return sc != nil && sc.dropped == 1
	})

	out, err := callTool(t, m.screencastStop, `{}`)
	if err != nil {
		t.Fatalf("screencast_stop: %v", err)
	}
	if res := out.(map[string]any); res["truncated"] != "maxDurationMs" {
		t.Errorf("truncated = %v, want maxDurationMs", res["truncated"])
	}
}

func TestScreencastRejectsNonGIFPath(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)

	_, err := callTool(t, m.screencastStart, `{"filePath":"/tmp/x.webm"}`)
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Code != toolerr.CodeInvalidArguments {
		t.Fatalf("want invalid_arguments for .webm, got %v", err)
	}
}
