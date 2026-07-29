package tools

import (
	"context"
	"encoding/json"
)

// Input dispatch while a JavaScript dialog is open.
//
// alert/confirm/prompt block the renderer's main thread, so a CDP call that
// triggers one never gets a reply until the dialog is handled. Waiting for
// that reply made a click that had actually worked look like a 30s timeout
// (found driving the server as an MCP client). Instead, every call that can
// open a dialog races the reply against the dialog-opened event and returns
// as soon as either arrives, telling the agent to call handle_dialog.

// openDialog reports the dialog currently open on the session, if any.
func (m *Manager) openDialog(sessionID string) *dialogState {
	m.col.mu.Lock()
	defer m.col.mu.Unlock()
	return m.col.dialogs[sessionID]
}

// callGuarded issues a CDP call that may open a JavaScript dialog. It
// returns the dialog when one interrupted the call; the call itself is then
// abandoned (its late reply is discarded by the client).
func (m *Manager) callGuarded(ctx context.Context, sessionID, method string, params, result any) (*dialogState, error) {
	// A dialog left open by an earlier action blocks this call before it
	// even starts, so report it rather than hanging.
	if d := m.openDialog(sessionID); d != nil {
		return d, nil
	}

	opened := m.addWaiter(sessionID, "Page.javascriptDialogOpening")
	defer m.removeWaiter(sessionID, "Page.javascriptDialogOpening", opened)

	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- m.client.Call(callCtx, sessionID, method, params, result) }()

	select {
	case err := <-done:
		return nil, err
	case raw := <-opened:
		var d dialogState
		_ = json.Unmarshal(raw, &d)
		return &d, nil
	case <-ctx.Done():
		return nil, mapErr(ctx.Err())
	}
}

// withDialogNote annotates a tool result when a dialog interrupted it, so
// the agent learns both that the action landed and what to do next.
func withDialogNote(result map[string]any, d *dialogState) map[string]any {
	if d == nil {
		return result
	}
	result["dialogOpen"] = map[string]any{"type": d.Type, "message": d.Message}
	result["note"] = "a JavaScript dialog is open and the page is blocked; call handle_dialog to continue"
	return result
}
