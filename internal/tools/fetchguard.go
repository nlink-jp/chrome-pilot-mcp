package tools

import (
	"context"
	"encoding/json"
)

// CDP-level enforcement of the host lists (ADR-0001).
//
// Fetch.requestPaused fires for every request the page makes — including
// redirects, in-page fetch/XHR, and subresources — which is what makes
// this the real boundary rather than the tool-argument checks. Requests
// to non-permitted hosts are failed with BlockedByClient, which also
// surfaces in the network collector so a blocked load is visible in
// list_network_requests.

// enableFetchGuard installs the interception on a freshly attached
// session. It is a no-op when no restriction is configured, so the
// unrestricted path keeps its original zero-overhead behavior.
func (m *Manager) enableFetchGuardLocked(ctx context.Context, sessionID string) error {
	if !m.filter.active() {
		return nil
	}
	return m.client.Call(ctx, sessionID, "Fetch.enable", map[string]any{
		"patterns": []map[string]any{{"urlPattern": "*"}},
	}, nil)
}

// handleRequestPaused answers one intercepted request.
//
// Called from the CDP read loop, so the CDP reply goes through a
// goroutine — same rule as the screencast frame ack.
func (m *Manager) handleRequestPaused(sessionID string, params json.RawMessage) {
	var p struct {
		RequestID string `json:"requestId"`
		Request   struct {
			URL string `json:"url"`
		} `json:"request"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	allowed, _ := m.filter.urlAllowed(p.Request.URL)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), defaultCallTimeout)
		defer cancel()
		if allowed {
			_ = m.client.Call(ctx, sessionID, "Fetch.continueRequest",
				map[string]any{"requestId": p.RequestID}, nil)
			return
		}
		m.logger.Warn("blocked request", "url", p.Request.URL)
		_ = m.client.Call(ctx, sessionID, "Fetch.failRequest",
			map[string]any{"requestId": p.RequestID, "errorReason": "BlockedByClient"}, nil)
	}()
}
