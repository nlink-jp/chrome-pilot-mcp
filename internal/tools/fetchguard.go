package tools

import (
	"context"
	"encoding/json"
)

// CDP-level enforcement of the host lists (ADR-0001) and of local files
// (ADR-0008).
//
// Fetch.requestPaused fires for every request the page makes — including
// redirects, in-page fetch/XHR, and subresources — and for every file://
// load, JavaScript navigation and view-source: included (measured
// 2026-09-22), which is what makes this the real boundary rather than the
// tool-argument checks. What is not permitted is failed with
// BlockedByClient, which also surfaces in the network collector so a
// blocked load is visible in list_network_requests.

// enableFetchGuard installs the interception on a freshly attached session.
// It is always on, for local files: with no host list the pattern is
// file://* alone, and no http request pauses (measured), so the default path
// keeps its zero overhead on the web.
func (m *Manager) enableFetchGuardLocked(ctx context.Context, sessionID string) error {
	pattern := "file://*"
	if m.filter.active() {
		pattern = "*"
	}
	return m.client.Call(ctx, sessionID, "Fetch.enable", map[string]any{
		"patterns": []map[string]any{{"urlPattern": pattern}},
	}, nil)
}

// requestAllowed decides one intercepted request: a local file by the grants
// of its session, anything else by the host lists (everything, when none is
// configured). It must not take m.mu — see localfiles.go.
func (m *Manager) requestAllowed(sessionID, rawURL string) bool {
	if _, local, _ := localPath(rawURL); local {
		return m.fileRequestAllowed(sessionID, rawURL)
	}
	if !m.filter.active() {
		return true
	}
	ok, _ := m.filter.urlAllowed(rawURL)
	return ok
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
	go func() {
		// Decided here, off the CDP read loop: judging a local file stats
		// the file system.
		allowed := m.requestAllowed(sessionID, p.Request.URL)
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
