package tools

import (
	"context"
	"encoding/json"
	"fmt"
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

	if args.URL != "" {
		if err := m.navigateSession(ctx, p.sessionID, args.URL, timeoutFromMS(args.Timeout, defaultNavigateTimeout)); err != nil {
			return nil, err
		}
	}
	return map[string]any{"index": m.indexOf(res.TargetID), "url": args.URL}, nil
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
	if err := m.navigateSession(ctx, p.sessionID, args.URL, timeoutFromMS(args.Timeout, defaultNavigateTimeout)); err != nil {
		return nil, err
	}
	return map[string]any{"url": args.URL, "loaded": true}, nil
}

// navigateSession navigates and waits for the load event.
func (m *Manager) navigateSession(ctx context.Context, sessionID, url string, timeout time.Duration) error {
	loaded := m.addWaiter(sessionID, "Page.loadEventFired")
	defer m.removeWaiter(sessionID, "Page.loadEventFired", loaded)

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var res struct {
		ErrorText string `json:"errorText"`
	}
	if err := m.client.Call(callCtx, sessionID, "Page.navigate", map[string]any{"url": url}, &res); err != nil {
		return err
	}
	if res.ErrorText != "" {
		return toolerr.Newf(toolerr.CodeCDPError, "navigation to %s failed: %s", url, res.ErrorText)
	}

	select {
	case <-loaded:
		return nil
	case <-time.After(timeout):
		return toolerr.Newf(toolerr.CodeTimeout, "load event not fired within %s for %s", timeout, url)
	case <-ctx.Done():
		return mapErr(ctx.Err())
	}
}

func (m *Manager) waitFor(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Text    string `json:"text"`
		Timeout int    `json:"timeout"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if args.Text == "" {
		return nil, toolerr.New(toolerr.CodeMissingArgument, "text is required")
	}
	p, err := m.selectedPage(ctx)
	if err != nil {
		return nil, err
	}

	timeout := timeoutFromMS(args.Timeout, defaultWaitForTimeout)
	deadline := time.Now().Add(timeout)
	textJSON, _ := json.Marshal(args.Text)
	expr := fmt.Sprintf("!!document.body && document.body.innerText.includes(%s)", textJSON)

	for {
		found, err := m.evalBool(ctx, p.sessionID, expr)
		if err != nil {
			return nil, err
		}
		if found {
			return map[string]any{"found": true, "text": args.Text}, nil
		}
		if time.Now().After(deadline) {
			return nil, toolerr.Newf(toolerr.CodeTimeout, "text %q did not appear within %s", args.Text, timeout)
		}
		select {
		case <-time.After(waitForPollInterval):
		case <-ctx.Done():
			return nil, mapErr(ctx.Err())
		}
	}
}

func (m *Manager) evalBool(ctx context.Context, sessionID, expr string) (bool, error) {
	callCtx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()
	var res struct {
		Result struct {
			Value bool `json:"value"`
		} `json:"result"`
	}
	err := m.client.Call(callCtx, sessionID, "Runtime.evaluate", map[string]any{
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
