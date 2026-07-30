package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nlink-jp/chrome-pilot-mcp/internal/toolerr"
)

const (
	defaultNavigateTimeout = 15 * time.Second
	defaultWaitForTimeout  = 15 * time.Second
	waitForPollInterval    = 200 * time.Millisecond
)

type pageEntry struct {
	Index    int    `json:"index"`
	URL      string `json:"url"`
	Title    string `json:"title"`
	Selected bool   `json:"selected"`
}

func (m *Manager) listPages(ctx context.Context, _ json.RawMessage) (any, error) {
	if err := m.ensure(ctx); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.refreshPagesLocked(ctx); err != nil {
		return nil, err
	}
	entries := make([]pageEntry, 0, len(m.pages))
	for i, p := range m.pages {
		entries = append(entries, pageEntry{
			Index:    i,
			URL:      p.url,
			Title:    p.title,
			Selected: p.targetID == m.selected,
		})
	}
	return map[string]any{"pages": entries}, nil
}

func (m *Manager) newPage(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		URL     string `json:"url"`
		Timeout int    `json:"timeout"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if args.URL != "" {
		if err := m.filter.checkURL(args.URL); err != nil {
			return nil, err
		}
	}
	if err := m.ensure(ctx); err != nil {
		return nil, err
	}

	m.mu.Lock()
	var res struct {
		TargetID string `json:"targetId"`
	}
	// Always create about:blank, then navigate through the common path so
	// load waiting works (the session must be attached before the load).
	err := m.client.Call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &res)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if err := m.refreshPagesLocked(ctx); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	m.selected = res.TargetID
	p := m.pageByTargetLocked(res.TargetID)
	if p == nil {
		m.mu.Unlock()
		return nil, toolerr.New(toolerr.CodePageNotFound, "created page disappeared")
	}
	if err := m.attachPageLocked(ctx, p); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	m.mu.Unlock()

	out := map[string]any{"index": m.indexOf(res.TargetID), "url": args.URL}
	if args.URL != "" {
		note, err := m.navigateSession(ctx, p.sessionID, args.URL, timeoutFromMS(args.Timeout, defaultNavigateTimeout))
		if err != nil {
			return nil, err
		}
		if note != "" {
			out["note"] = note
		}
	}
	return out, nil
}

func (m *Manager) indexOf(targetID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, p := range m.pages {
		if p.targetID == targetID {
			return i
		}
	}
	return -1
}

func (m *Manager) selectPage(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		PageIdx *int `json:"pageIdx"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if args.PageIdx == nil {
		return nil, toolerr.New(toolerr.CodeMissingArgument, "pageIdx is required")
	}
	if err := m.ensure(ctx); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.refreshPagesLocked(ctx); err != nil {
		return nil, err
	}
	i := *args.PageIdx
	if i < 0 || i >= len(m.pages) {
		return nil, toolerr.Newf(toolerr.CodePageNotFound, "no page at index %d (have %d)", i, len(m.pages))
	}
	m.selected = m.pages[i].targetID
	return map[string]any{"index": i, "url": m.pages[i].url}, nil
}

func (m *Manager) closePage(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		PageIdx *int `json:"pageIdx"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if args.PageIdx == nil {
		return nil, toolerr.New(toolerr.CodeMissingArgument, "pageIdx is required")
	}
	if err := m.ensure(ctx); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.refreshPagesLocked(ctx); err != nil {
		return nil, err
	}
	i := *args.PageIdx
	if i < 0 || i >= len(m.pages) {
		return nil, toolerr.Newf(toolerr.CodePageNotFound, "no page at index %d (have %d)", i, len(m.pages))
	}
	targetID := m.pages[i].targetID
	if err := m.client.Call(ctx, "", "Target.closeTarget", map[string]any{"targetId": targetID}, nil); err != nil {
		return nil, err
	}
	delete(m.pageEnabled, m.pages[i].sessionID)
	if err := m.refreshPagesLocked(ctx); err != nil {
		return nil, err
	}
	return map[string]any{"closed": i, "remaining": len(m.pages)}, nil
}

func (m *Manager) navigatePage(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		URL     string `json:"url"`
		Timeout int    `json:"timeout"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if args.URL == "" {
		return nil, toolerr.New(toolerr.CodeMissingArgument, "url is required")
	}
	if err := m.filter.checkURL(args.URL); err != nil {
		return nil, err
	}
	p, err := m.selectedPage(ctx)
	if err != nil {
		return nil, err
	}
	note, err := m.navigateSession(ctx, p.sessionID, args.URL, timeoutFromMS(args.Timeout, defaultNavigateTimeout))
	if err != nil {
		return nil, err
	}
	out := map[string]any{"url": args.URL, "loaded": true}
	if note != "" {
		out["note"] = note
	}
	return out, nil
}

// navigateSession navigates and waits for the load event.
//
// Missing the event is not the same as failing to navigate: a load event
// can be delayed or slip past the waiter, and reporting a timeout for a
// page that is in fact loaded is worse than useless — it tells the agent
// to retry work that already succeeded. On timeout the document's own
// readyState decides, and only a document that never got there is an error.
func (m *Manager) navigateSession(ctx context.Context, sessionID, url string, timeout time.Duration) (loadNote string, err error) {
	loaded := m.addWaiter(sessionID, "Page.loadEventFired")
	defer m.removeWaiter(sessionID, "Page.loadEventFired", loaded)

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var res struct {
		ErrorText string `json:"errorText"`
	}
	if err := m.client.Call(callCtx, sessionID, "Page.navigate", map[string]any{"url": url}, &res); err != nil {
		return "", err
	}
	if res.ErrorText != "" {
		return "", toolerr.Newf(toolerr.CodeCDPError, "navigation to %s failed: %s", url, res.ErrorText)
	}

	select {
	case <-loaded:
		return "", nil
	case <-time.After(timeout):
		state, stateErr := m.documentReadyState(ctx, sessionID)
		if stateErr == nil && (state == "complete" || state == "interactive") {
			return fmt.Sprintf("the load event was not observed within %s, but the document reached readyState %q", timeout, state), nil
		}
		return "", toolerr.Newf(toolerr.CodeTimeout,
			"load event not fired within %s for %s (document readyState %q)", timeout, url, state)
	case <-ctx.Done():
		return "", mapErr(ctx.Err())
	}
}

// documentReadyState reads document.readyState, used to second-guess a
// missed load event.
func (m *Manager) documentReadyState(ctx context.Context, sessionID string) (string, error) {
	callCtx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()
	var res struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	err := m.rendererCall(callCtx, sessionID, "Runtime.evaluate", map[string]any{
		"expression":    "document.readyState",
		"returnByValue": true,
	}, &res)
	if err != nil {
		return "", err
	}
	return res.Result.Value, nil
}

// waitFor blocks until a condition holds on the selected page.
//
// The condition is either text appearing in the page, or a CSS selector
// reaching a state. Waiting for an element to appear or (just as often) to
// go away — a spinner, an overlay — is the common need in automation that
// text matching alone cannot express.
func (m *Manager) waitFor(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Text     string `json:"text"`
		Selector string `json:"selector"`
		State    string `json:"state"`
		Timeout  int    `json:"timeout"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if args.Text == "" && args.Selector == "" {
		return nil, toolerr.New(toolerr.CodeMissingArgument, "one of text or selector is required")
	}
	if args.Text != "" && args.Selector != "" {
		return nil, toolerr.New(toolerr.CodeInvalidArguments, "text and selector are mutually exclusive")
	}

	expr, describe, err := waitExpression(args.Text, args.Selector, args.State)
	if err != nil {
		return nil, err
	}

	p, err := m.selectedPage(ctx)
	if err != nil {
		return nil, err
	}

	timeout := timeoutFromMS(args.Timeout, defaultWaitForTimeout)
	deadline := time.Now().Add(timeout)
	for {
		ok, err := m.evalBool(ctx, p.sessionID, expr)
		if err != nil {
			return nil, err
		}
		if ok {
			out := map[string]any{"found": true}
			if args.Text != "" {
				out["text"] = args.Text
			} else {
				out["selector"] = args.Selector
				out["state"] = describe
			}
			return out, nil
		}
		if time.Now().After(deadline) {
			return nil, toolerr.Newf(toolerr.CodeTimeout, "%s within %s", describeTimeout(args.Text, args.Selector, describe), timeout)
		}
		select {
		case <-time.After(waitForPollInterval):
		case <-ctx.Done():
			return nil, mapErr(ctx.Err())
		}
	}
}

// waitStates maps the selector states to the JS predicate that decides them.
// "visible"/"hidden" consider layout, so an element rendered with
// display:none counts as hidden even though it is present in the DOM.
var waitStates = map[string]string{
	"visible": `(() => { const e = document.querySelector(SEL); if (!e) return false;
		const r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0 &&
		getComputedStyle(e).visibility !== 'hidden' })()`,
	"hidden": `(() => { const e = document.querySelector(SEL); if (!e) return true;
		const r = e.getBoundingClientRect(); return !(r.width > 0 && r.height > 0) ||
		getComputedStyle(e).visibility === 'hidden' })()`,
	"present": `!!document.querySelector(SEL)`,
	"absent":  `!document.querySelector(SEL)`,
}

// waitExpression builds the polled predicate and a human description.
func waitExpression(text, selector, state string) (expr, describe string, err error) {
	if text != "" {
		textJSON, _ := json.Marshal(text)
		return fmt.Sprintf("!!document.body && document.body.innerText.includes(%s)", textJSON), "text", nil
	}
	if state == "" {
		state = "visible"
	}
	tmpl, ok := waitStates[state]
	if !ok {
		return "", "", toolerr.Newf(toolerr.CodeInvalidArguments,
			"state must be visible, hidden, present, or absent (got %q)", state)
	}
	selJSON, _ := json.Marshal(selector)
	return strings.ReplaceAll(tmpl, "SEL", string(selJSON)), state, nil
}

func describeTimeout(text, selector, state string) string {
	if text != "" {
		return fmt.Sprintf("text %q did not appear", text)
	}
	return fmt.Sprintf("selector %q did not become %s", selector, state)
}

func (m *Manager) evalBool(ctx context.Context, sessionID, expr string) (bool, error) {
	callCtx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()
	var res struct {
		Result struct {
			Value bool `json:"value"`
		} `json:"result"`
	}
	err := m.rendererCall(callCtx, sessionID, "Runtime.evaluate", map[string]any{
		"expression":    expr,
		"returnByValue": true,
	}, &res)
	if err != nil {
		return false, err
	}
	return res.Result.Value, nil
}

func timeoutFromMS(ms int, def time.Duration) time.Duration {
	if ms <= 0 {
		return def
	}
	return time.Duration(ms) * time.Millisecond
}
