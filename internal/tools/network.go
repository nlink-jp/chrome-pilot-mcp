package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nlink-jp/chrome-pilot-mcp/internal/toolerr"
)

// bodyCharBudget caps an inline response body.
const bodyCharBudget = 50_000

func (m *Manager) listNetworkRequests(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		PageSize      int      `json:"pageSize"`
		PageIdx       int      `json:"pageIdx"`
		ResourceTypes []string `json:"resourceTypes"`
		SinceReqID    int      `json:"sinceReqId"`
		FailedOnly    bool     `json:"failedOnly"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	p, err := m.selectedPage(ctx)
	if err != nil {
		return nil, err
	}

	typeOK := func(t string) bool {
		if len(args.ResourceTypes) == 0 {
			return true
		}
		for _, want := range args.ResourceTypes {
			if t == want {
				return true
			}
		}
		return false
	}

	m.col.mu.Lock()
	var reqs []*netRequest
	lastID := 0
	for _, r := range m.col.netReqs {
		if r.sessionID != p.sessionID {
			continue
		}
		if r.ID > lastID {
			lastID = r.ID
		}
		if r.ID <= args.SinceReqID || !typeOK(r.ResourceType) {
			continue
		}
		// failedOnly narrows to the requests that actually went wrong —
		// including the ones the host filter blocked.
		if args.FailedOnly && r.Failed == "" && r.Status < 400 {
			continue
		}
		reqs = append(reqs, r)
	}
	m.col.mu.Unlock()

	total := len(reqs)
	reqs = paginate(reqs, args.PageSize, args.PageIdx)
	return map[string]any{"total": total, "requests": reqs, "lastReqId": lastID}, nil
}

func (m *Manager) getNetworkRequest(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		ReqID *int `json:"reqid"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if args.ReqID == nil {
		return nil, toolerr.New(toolerr.CodeMissingArgument, "reqid is required")
	}

	m.col.mu.Lock()
	var req *netRequest
	for _, r := range m.col.netReqs {
		if r.ID == *args.ReqID {
			req = r
			break
		}
	}
	m.col.mu.Unlock()
	if req == nil {
		return nil, toolerr.Newf(toolerr.CodeInvalidArguments, "no network request with reqid %d (it may have been evicted)", *args.ReqID)
	}

	out := map[string]any{
		"reqid": req.ID, "url": req.URL, "method": req.Method,
		"resourceType": req.ResourceType, "status": req.Status,
		"mimeType": req.MimeType, "finished": req.Finished, "size": req.Size,
	}
	if req.Failed != "" {
		out["failed"] = req.Failed
	}
	if len(req.reqHeaders) > 0 {
		out["requestHeaders"] = req.reqHeaders
	}
	if len(req.respHeaders) > 0 {
		out["responseHeaders"] = req.respHeaders
	}

	// Response body is fetched on demand; Chrome may have evicted it.
	if req.Finished && req.Failed == "" {
		callCtx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
		var body struct {
			Body          string `json:"body"`
			Base64Encoded bool   `json:"base64Encoded"`
		}
		err := m.client.Call(callCtx, req.sessionID, "Network.getResponseBody",
			map[string]any{"requestId": req.requestID}, &body)
		cancel()
		switch {
		case err != nil:
			out["bodyError"] = fmt.Sprintf("response body unavailable: %v", err)
		case body.Base64Encoded:
			out["bodyNote"] = fmt.Sprintf("binary body omitted (%d base64 chars, mimeType %s)", len(body.Body), req.MimeType)
		case len(body.Body) > bodyCharBudget:
			out["body"] = body.Body[:bodyCharBudget]
			out["bodyNote"] = fmt.Sprintf("body truncated to %d of %d chars", bodyCharBudget, len(body.Body))
		default:
			out["body"] = body.Body
		}
	}
	return out, nil
}
