package tools

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"github.com/nlink-jp/chrome-pilot-mcp/internal/mcpserver"
	"github.com/nlink-jp/chrome-pilot-mcp/internal/toolerr"
)

// resolveUID looks up a snapshot uid. Unknown uids get a hint to re-snapshot
// (uids are invalidated by every take_snapshot).
func (m *Manager) resolveUID(uid string) (uidTarget, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.uids[uid]
	if !ok {
		return uidTarget{}, toolerr.Newf(toolerr.CodeElementNotFound,
			"unknown uid %q; call take_snapshot and use a uid from the fresh snapshot", uid)
	}
	return t, nil
}

// elementCenter scrolls the node into view and returns the center of its
// content box in CSS pixels.
func (m *Manager) elementCenter(ctx context.Context, sessionID string, backendNodeID int64) (float64, float64, error) {
	// Best effort; some nodes (e.g. options) do not support it.
	_ = m.client.Call(ctx, sessionID, "DOM.scrollIntoViewIfNeeded",
		map[string]any{"backendNodeId": backendNodeID}, nil)

	var res struct {
		Model struct {
			Content []float64 `json:"content"`
		} `json:"model"`
	}
	err := m.client.Call(ctx, sessionID, "DOM.getBoxModel",
		map[string]any{"backendNodeId": backendNodeID}, &res)
	if err != nil {
		return 0, 0, err
	}
	q := res.Model.Content
	if len(q) != 8 {
		return 0, 0, toolerr.New(toolerr.CodeElementNotFound, "element has no layout box (hidden?)")
	}
	return (q[0] + q[2] + q[4] + q[6]) / 4, (q[1] + q[3] + q[5] + q[7]) / 4, nil
}

// mouseClickAt dispatches a click (or double click) at x,y.
func (m *Manager) mouseClickAt(ctx context.Context, sessionID string, x, y float64, dblClick bool) error {
	move := map[string]any{"type": "mouseMoved", "x": x, "y": y, "button": "none"}
	if err := m.client.Call(ctx, sessionID, "Input.dispatchMouseEvent", move, nil); err != nil {
		return err
	}
	clicks := 1
	if dblClick {
		clicks = 2
	}
	for i := 1; i <= clicks; i++ {
		for _, typ := range []string{"mousePressed", "mouseReleased"} {
			ev := map[string]any{
				"type": typ, "x": x, "y": y,
				"button": "left", "clickCount": i,
			}
			if err := m.client.Call(ctx, sessionID, "Input.dispatchMouseEvent", ev, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// finishInput builds the tool result, appending a fresh snapshot when asked.
func (m *Manager) finishInput(ctx context.Context, p *pageState, result map[string]any, includeSnapshot bool) (any, error) {
	body, _ := json.Marshal(result)
	if !includeSnapshot {
		return mcpserver.RawResult{Content: []mcpserver.ContentBlock{
			{Type: "text", Text: string(body)},
		}}, nil
	}
	text, err := m.snapshotText(ctx, p)
	if err != nil {
		return nil, err
	}
	return mcpserver.RawResult{Content: []mcpserver.ContentBlock{
		{Type: "text", Text: string(body)},
		{Type: "text", Text: text},
	}}, nil
}

// uidCenter resolves a uid on the selected page and returns its center.
func (m *Manager) uidCenter(ctx context.Context, uid string) (*pageState, float64, float64, error) {
	p, err := m.selectedPage(ctx)
	if err != nil {
		return nil, 0, 0, err
	}
	t, err := m.resolveUID(uid)
	if err != nil {
		return nil, 0, 0, err
	}
	callCtx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()
	x, y, err := m.elementCenter(callCtx, t.sessionID, t.backendNodeID)
	if err != nil {
		return nil, 0, 0, err
	}
	return p, x, y, nil
}

func (m *Manager) click(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		UID             string `json:"uid"`
		DblClick        bool   `json:"dblClick"`
		IncludeSnapshot bool   `json:"includeSnapshot"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if args.UID == "" {
		return nil, toolerr.New(toolerr.CodeMissingArgument, "uid is required")
	}
	p, x, y, err := m.uidCenter(ctx, args.UID)
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()
	if err := m.mouseClickAt(callCtx, sessionOf(p), x, y, args.DblClick); err != nil {
		return nil, err
	}
	return m.finishInput(ctx, p, map[string]any{"clicked": args.UID, "dblClick": args.DblClick}, args.IncludeSnapshot)
}

func sessionOf(p *pageState) string { return p.sessionID }

func (m *Manager) clickAt(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		X               *float64 `json:"x"`
		Y               *float64 `json:"y"`
		DblClick        bool     `json:"dblClick"`
		IncludeSnapshot bool     `json:"includeSnapshot"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if args.X == nil || args.Y == nil {
		return nil, toolerr.New(toolerr.CodeMissingArgument, "x and y are required")
	}
	p, err := m.selectedPage(ctx)
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()
	if err := m.mouseClickAt(callCtx, p.sessionID, *args.X, *args.Y, args.DblClick); err != nil {
		return nil, err
	}
	return m.finishInput(ctx, p, map[string]any{"clickedAt": []float64{*args.X, *args.Y}}, args.IncludeSnapshot)
}

func (m *Manager) hover(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		UID             string `json:"uid"`
		IncludeSnapshot bool   `json:"includeSnapshot"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if args.UID == "" {
		return nil, toolerr.New(toolerr.CodeMissingArgument, "uid is required")
	}
	p, x, y, err := m.uidCenter(ctx, args.UID)
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()
	move := map[string]any{"type": "mouseMoved", "x": x, "y": y, "button": "none"}
	if err := m.client.Call(callCtx, p.sessionID, "Input.dispatchMouseEvent", move, nil); err != nil {
		return nil, err
	}
	return m.finishInput(ctx, p, map[string]any{"hovered": args.UID}, args.IncludeSnapshot)
}

// drag performs a mouse-based drag from one element to another. HTML5
// drag-and-drop (dragstart/drop event based UIs) is not simulated; sliders
// and mouse-tracking UIs work.
func (m *Manager) drag(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		FromUID         string `json:"from_uid"`
		ToUID           string `json:"to_uid"`
		IncludeSnapshot bool   `json:"includeSnapshot"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if args.FromUID == "" || args.ToUID == "" {
		return nil, toolerr.New(toolerr.CodeMissingArgument, "from_uid and to_uid are required")
	}
	p, fx, fy, err := m.uidCenter(ctx, args.FromUID)
	if err != nil {
		return nil, err
	}
	to, err := m.resolveUID(args.ToUID)
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()
	tx, ty, err := m.elementCenter(callCtx, to.sessionID, to.backendNodeID)
	if err != nil {
		return nil, err
	}

	sess := p.sessionID
	steps := []map[string]any{
		{"type": "mouseMoved", "x": fx, "y": fy, "button": "none"},
		{"type": "mousePressed", "x": fx, "y": fy, "button": "left", "clickCount": 1},
		{"type": "mouseMoved", "x": (fx + tx) / 2, "y": (fy + ty) / 2, "button": "left"},
		{"type": "mouseMoved", "x": tx, "y": ty, "button": "left"},
		{"type": "mouseReleased", "x": tx, "y": ty, "button": "left", "clickCount": 1},
	}
	for _, ev := range steps {
		if err := m.client.Call(callCtx, sess, "Input.dispatchMouseEvent", ev, nil); err != nil {
			return nil, err
		}
	}
	return m.finishInput(ctx, p, map[string]any{"dragged": args.FromUID, "onto": args.ToUID}, args.IncludeSnapshot)
}

// fillFunction is evaluated on the element; it uses the native value setter
// so framework change-tracking (React etc.) observes the update.
const fillFunction = `function(value) {
	const el = this;
	const tag = (el.tagName || '').toLowerCase();
	const fire = () => {
		el.dispatchEvent(new Event('input', {bubbles: true}));
		el.dispatchEvent(new Event('change', {bubbles: true}));
	};
	if (tag === 'select') { el.value = value; fire(); return; }
	if (tag === 'input' && (el.type === 'checkbox' || el.type === 'radio')) {
		const want = value === 'true';
		if (el.checked !== want) el.click();
		return;
	}
	if (tag === 'input' || tag === 'textarea') {
		el.focus();
		const proto = tag === 'textarea' ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
		const desc = Object.getOwnPropertyDescriptor(proto, 'value');
		if (desc && desc.set) desc.set.call(el, value); else el.value = value;
		fire();
		return;
	}
	if (el.isContentEditable) {
		el.focus();
		el.textContent = value;
		el.dispatchEvent(new Event('input', {bubbles: true}));
		return;
	}
	throw new Error('element <' + tag + '> is not fillable');
}`

// fillElement fills one uid with a value.
func (m *Manager) fillElement(ctx context.Context, uid, value string) error {
	t, err := m.resolveUID(uid)
	if err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()

	var resolved struct {
		Object struct {
			ObjectID string `json:"objectId"`
		} `json:"object"`
	}
	err = m.client.Call(callCtx, t.sessionID, "DOM.resolveNode",
		map[string]any{"backendNodeId": t.backendNodeID}, &resolved)
	if err != nil {
		return err
	}

	var res struct {
		ExceptionDetails *struct {
			Exception *struct {
				Description string `json:"description"`
			} `json:"exception"`
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	err = m.client.Call(callCtx, t.sessionID, "Runtime.callFunctionOn", map[string]any{
		"objectId":            resolved.Object.ObjectID,
		"functionDeclaration": fillFunction,
		"arguments":           []map[string]any{{"value": value}},
	}, &res)
	if err != nil {
		return err
	}
	if res.ExceptionDetails != nil {
		desc := res.ExceptionDetails.Text
		if res.ExceptionDetails.Exception != nil && res.ExceptionDetails.Exception.Description != "" {
			desc = res.ExceptionDetails.Exception.Description
		}
		return toolerr.Newf(toolerr.CodeScriptFailed, "fill %s: %s", uid, desc)
	}
	return nil
}

func (m *Manager) fill(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		UID             string  `json:"uid"`
		Value           *string `json:"value"`
		IncludeSnapshot bool    `json:"includeSnapshot"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if args.UID == "" || args.Value == nil {
		return nil, toolerr.New(toolerr.CodeMissingArgument, "uid and value are required")
	}
	p, err := m.selectedPage(ctx)
	if err != nil {
		return nil, err
	}
	if err := m.fillElement(ctx, args.UID, *args.Value); err != nil {
		return nil, err
	}
	return m.finishInput(ctx, p, map[string]any{"filled": args.UID}, args.IncludeSnapshot)
}

func (m *Manager) fillForm(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Elements []struct {
			UID   string `json:"uid"`
			Value string `json:"value"`
		} `json:"elements"`
		IncludeSnapshot bool `json:"includeSnapshot"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if len(args.Elements) == 0 {
		return nil, toolerr.New(toolerr.CodeMissingArgument, "elements is required")
	}
	p, err := m.selectedPage(ctx)
	if err != nil {
		return nil, err
	}
	for _, e := range args.Elements {
		if err := m.fillElement(ctx, e.UID, e.Value); err != nil {
			return nil, err
		}
	}
	return m.finishInput(ctx, p, map[string]any{"filled": len(args.Elements)}, args.IncludeSnapshot)
}

func (m *Manager) typeText(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Text      *string `json:"text"`
		SubmitKey string  `json:"submitKey"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if args.Text == nil {
		return nil, toolerr.New(toolerr.CodeMissingArgument, "text is required")
	}
	p, err := m.selectedPage(ctx)
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()
	if err := m.client.Call(callCtx, p.sessionID, "Input.insertText", map[string]any{"text": *args.Text}, nil); err != nil {
		return nil, err
	}
	if args.SubmitKey != "" {
		if err := m.pressKeyCombo(callCtx, p.sessionID, args.SubmitKey); err != nil {
			return nil, err
		}
	}
	return map[string]any{"typed": *args.Text}, nil
}

func (m *Manager) pressKey(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Key             string `json:"key"`
		IncludeSnapshot bool   `json:"includeSnapshot"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if args.Key == "" {
		return nil, toolerr.New(toolerr.CodeMissingArgument, "key is required")
	}
	p, err := m.selectedPage(ctx)
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()
	if err := m.pressKeyCombo(callCtx, p.sessionID, args.Key); err != nil {
		return nil, err
	}
	return m.finishInput(ctx, p, map[string]any{"pressed": args.Key}, args.IncludeSnapshot)
}

func (m *Manager) uploadFile(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		UID             string `json:"uid"`
		FilePath        string `json:"filePath"`
		IncludeSnapshot bool   `json:"includeSnapshot"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if args.UID == "" || args.FilePath == "" {
		return nil, toolerr.New(toolerr.CodeMissingArgument, "uid and filePath are required")
	}
	if _, err := os.Stat(args.FilePath); err != nil {
		return nil, toolerr.Newf(toolerr.CodeInvalidArguments, "filePath: %v", err)
	}
	p, err := m.selectedPage(ctx)
	if err != nil {
		return nil, err
	}
	t, err := m.resolveUID(args.UID)
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()
	err = m.client.Call(callCtx, t.sessionID, "DOM.setFileInputFiles", map[string]any{
		"files":         []string{args.FilePath},
		"backendNodeId": t.backendNodeID,
	}, nil)
	if err != nil {
		return nil, err
	}
	return m.finishInput(ctx, p, map[string]any{"uploaded": args.FilePath, "to": args.UID}, args.IncludeSnapshot)
}

func (m *Manager) handleDialog(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Action     string `json:"action"`
		PromptText string `json:"promptText"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if args.Action != "accept" && args.Action != "dismiss" {
		return nil, toolerr.Newf(toolerr.CodeInvalidArguments, "action must be accept or dismiss, got %q", args.Action)
	}
	// Do not use selectedPage here: it refreshes via CDP calls that can be
	// blocked while a dialog is open on some targets. Use the cached session.
	m.mu.Lock()
	p := m.pageByTargetLocked(m.selected)
	m.mu.Unlock()
	if p == nil || p.sessionID == "" {
		return nil, toolerr.New(toolerr.CodeDialogNotOpen, "no attached page")
	}

	m.col.mu.Lock()
	dlg := m.col.dialogs[p.sessionID]
	m.col.mu.Unlock()
	if dlg == nil {
		return nil, toolerr.New(toolerr.CodeDialogNotOpen, "no dialog is open on the selected page")
	}

	params := map[string]any{"accept": args.Action == "accept"}
	if args.PromptText != "" {
		params["promptText"] = args.PromptText
	}
	callCtx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()
	if err := m.client.Call(callCtx, p.sessionID, "Page.handleJavaScriptDialog", params, nil); err != nil {
		return nil, err
	}
	m.col.mu.Lock()
	delete(m.col.dialogs, p.sessionID)
	m.col.mu.Unlock()
	return map[string]any{"handled": args.Action, "dialogType": dlg.Type, "message": dlg.Message}, nil
}

// ---- key dispatch ----

type keyDef struct {
	key     string
	code    string
	keyCode int
	text    string
}

// namedKeys covers the non-printable keys agents actually use.
var namedKeys = map[string]keyDef{
	"enter":      {"Enter", "Enter", 13, "\r"},
	"tab":        {"Tab", "Tab", 9, ""},
	"escape":     {"Escape", "Escape", 27, ""},
	"backspace":  {"Backspace", "Backspace", 8, ""},
	"delete":     {"Delete", "Delete", 46, ""},
	"arrowup":    {"ArrowUp", "ArrowUp", 38, ""},
	"arrowdown":  {"ArrowDown", "ArrowDown", 40, ""},
	"arrowleft":  {"ArrowLeft", "ArrowLeft", 37, ""},
	"arrowright": {"ArrowRight", "ArrowRight", 39, ""},
	"home":       {"Home", "Home", 36, ""},
	"end":        {"End", "End", 35, ""},
	"pageup":     {"PageUp", "PageUp", 33, ""},
	"pagedown":   {"PageDown", "PageDown", 34, ""},
	"space":      {" ", "Space", 32, " "},
}

var modifierBits = map[string]int{
	"alt": 1, "control": 2, "ctrl": 2, "meta": 4, "cmd": 4, "shift": 8,
}

// parseKeyCombo splits "Control+Shift+R" (also "Control++" for the '+' key)
// into modifier bits and the final key definition.
func parseKeyCombo(combo string) (int, keyDef, error) {
	// "Control++" → modifier prefix "Control", key "+".
	keyPart := combo
	modPart := ""
	if i := strings.LastIndex(combo, "+"); i > 0 {
		modPart, keyPart = combo[:i], combo[i+1:]
		if keyPart == "" { // trailing '+' means the key is '+'
			keyPart = "+"
			modPart = strings.TrimSuffix(modPart, "+")
		}
	}
	mods := 0
	if modPart != "" {
		for _, tok := range strings.Split(modPart, "+") {
			bit, ok := modifierBits[strings.ToLower(tok)]
			if !ok {
				return 0, keyDef{}, toolerr.Newf(toolerr.CodeInvalidArguments, "unknown modifier %q in %q", tok, combo)
			}
			mods |= bit
		}
	}
	if def, ok := namedKeys[strings.ToLower(keyPart)]; ok {
		return mods, def, nil
	}
	r := []rune(keyPart)
	if len(r) != 1 {
		return 0, keyDef{}, toolerr.Newf(toolerr.CodeInvalidArguments, "unknown key %q in %q", keyPart, combo)
	}
	ch := string(r[0])
	upper := strings.ToUpper(ch)
	return mods, keyDef{key: ch, code: "Key" + upper, keyCode: int(upper[0]), text: ch}, nil
}

// pressKeyCombo dispatches keyDown+keyUp for a combo like "Control+A".
func (m *Manager) pressKeyCombo(ctx context.Context, sessionID, combo string) error {
	mods, def, err := parseKeyCombo(combo)
	if err != nil {
		return err
	}
	down := map[string]any{
		"type": "rawKeyDown", "modifiers": mods,
		"key": def.key, "code": def.code,
		"windowsVirtualKeyCode": def.keyCode, "nativeVirtualKeyCode": def.keyCode,
	}
	// Plain printable keys (no Control/Meta) produce text.
	if def.text != "" && mods&(2|4) == 0 {
		down["type"] = "keyDown"
		down["text"] = def.text
	}
	if err := m.client.Call(ctx, sessionID, "Input.dispatchKeyEvent", down, nil); err != nil {
		return err
	}
	up := map[string]any{
		"type": "keyUp", "modifiers": mods,
		"key": def.key, "code": def.code,
		"windowsVirtualKeyCode": def.keyCode, "nativeVirtualKeyCode": def.keyCode,
	}
	return m.client.Call(ctx, sessionID, "Input.dispatchKeyEvent", up, nil)
}
