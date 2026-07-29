package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nlink-jp/chrome-pilot-mcp/internal/mcpserver"
	"github.com/nlink-jp/chrome-pilot-mcp/internal/toolerr"
)

// snapshotCharBudget caps take_snapshot output so a huge page cannot flood
// the client context (truncated with a note).
const snapshotCharBudget = 100_000

// inlineImageBudget caps the base64 payload returned inline as MCP image
// content; larger screenshots are file-only.
const inlineImageBudget = 4 << 20

func (m *Manager) evaluateScript(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Function string `json:"function"`
		Timeout  int    `json:"timeout"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if args.Function == "" {
		return nil, toolerr.New(toolerr.CodeMissingArgument, "function is required")
	}
	p, err := m.selectedPage(ctx)
	if err != nil {
		return nil, err
	}

	callCtx, cancel := context.WithTimeout(ctx, timeoutFromMS(args.Timeout, defaultCallTimeout))
	defer cancel()
	var res struct {
		Result struct {
			Type        string          `json:"type"`
			Value       json.RawMessage `json:"value"`
			Description string          `json:"description"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception *struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	err = m.client.Call(callCtx, p.sessionID, "Runtime.evaluate", map[string]any{
		"expression":    "(" + args.Function + ")()",
		"returnByValue": true,
		"awaitPromise":  true,
		"userGesture":   true,
	}, &res)
	if err != nil {
		return nil, err
	}
	if res.ExceptionDetails != nil {
		desc := res.ExceptionDetails.Text
		if res.ExceptionDetails.Exception != nil && res.ExceptionDetails.Exception.Description != "" {
			desc = res.ExceptionDetails.Exception.Description
		}
		return nil, toolerr.New(toolerr.CodeScriptFailed, desc)
	}
	out := map[string]any{"type": res.Result.Type}
	if res.Result.Value != nil {
		out["value"] = res.Result.Value
	} else if res.Result.Description != "" {
		out["description"] = res.Result.Description
	}
	return out, nil
}

func (m *Manager) takeScreenshot(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Format   string `json:"format"`
		Quality  int    `json:"quality"`
		FullPage bool   `json:"fullPage"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	format := args.Format
	switch format {
	case "":
		format = "png"
	case "png", "jpeg":
	default:
		return nil, toolerr.Newf(toolerr.CodeInvalidArguments, "format must be png or jpeg, got %q", format)
	}

	p, err := m.selectedPage(ctx)
	if err != nil {
		return nil, err
	}

	params := map[string]any{"format": format}
	if format == "jpeg" && args.Quality > 0 {
		params["quality"] = args.Quality
	}
	if args.FullPage {
		params["captureBeyondViewport"] = true
	}
	callCtx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()
	var res struct {
		Data string `json:"data"`
	}
	if err := m.client.Call(callCtx, p.sessionID, "Page.captureScreenshot", params, &res); err != nil {
		return nil, err
	}
	img, err := base64.StdEncoding.DecodeString(res.Data)
	if err != nil {
		return nil, toolerr.Newf(toolerr.CodeCDPError, "decode screenshot data: %v", err)
	}

	ext := format
	if ext == "jpeg" {
		ext = "jpg"
	}
	name := fmt.Sprintf("shot-%s.%s", time.Now().Format("20060102-150405.000"), ext)
	path, err := m.workspaceFile("screenshots", name)
	if err != nil {
		return nil, toolerr.New(toolerr.CodeWorkspaceFailed, err.Error())
	}
	if err := os.WriteFile(path, img, 0o644); err != nil {
		return nil, toolerr.Newf(toolerr.CodeWorkspaceFailed, "write screenshot: %v", err)
	}

	meta, _ := json.Marshal(map[string]any{"path": path, "bytes": len(img), "format": format})
	content := []mcpserver.ContentBlock{{Type: "text", Text: string(meta)}}
	if len(res.Data) <= inlineImageBudget {
		content = append(content, mcpserver.ContentBlock{
			Type:     "image",
			Data:     res.Data,
			MimeType: "image/" + format,
		})
	}
	return mcpserver.RawResult{Content: content}, nil
}

// ---- take_snapshot ----

type axNode struct {
	NodeID           string   `json:"nodeId"`
	Ignored          bool     `json:"ignored"`
	Role             *axValue `json:"role"`
	Name             *axValue `json:"name"`
	Value            *axValue `json:"value"`
	ChildIDs         []string `json:"childIds"`
	BackendDOMNodeID int64    `json:"backendDOMNodeId"`
	ParentID         string   `json:"parentId"`
}

type axValue struct {
	Value any `json:"value"`
}

func (v *axValue) str() string {
	if v == nil {
		return ""
	}
	s, _ := v.Value.(string)
	return s
}

func (m *Manager) takeSnapshot(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct{}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	p, err := m.selectedPage(ctx)
	if err != nil {
		return nil, err
	}

	callCtx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()
	if err := m.client.Call(callCtx, p.sessionID, "Accessibility.enable", nil, nil); err != nil {
		return nil, err
	}
	var res struct {
		Nodes []axNode `json:"nodes"`
	}
	if err := m.client.Call(callCtx, p.sessionID, "Accessibility.getFullAXTree", nil, &res); err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.snapshotSeq++
	seq := m.snapshotSeq
	// A new snapshot invalidates old uids.
	m.uids = make(map[string]uidTarget)
	m.mu.Unlock()

	text, uids := formatAXTree(res.Nodes, seq)

	m.mu.Lock()
	for uid, backendID := range uids {
		m.uids[uid] = uidTarget{backendNodeID: backendID, sessionID: p.sessionID}
	}
	m.mu.Unlock()

	header := fmt.Sprintf("Page snapshot (url: %s)\n", p.url)
	return mcpserver.RawResult{Content: []mcpserver.ContentBlock{
		{Type: "text", Text: header + text},
	}}, nil
}

// formatAXTree renders the flat AX node list as an indented uid-tagged tree
// and returns the uid → backendDOMNodeId map.
func formatAXTree(nodes []axNode, seq int) (string, map[string]int64) {
	byID := make(map[string]axNode, len(nodes))
	isChild := make(map[string]bool)
	for _, n := range nodes {
		byID[n.NodeID] = n
		for _, c := range n.ChildIDs {
			isChild[c] = true
		}
	}

	var sb strings.Builder
	uids := make(map[string]int64)
	counter := 0
	truncated := false

	var walk func(id string, depth int)
	walk = func(id string, depth int) {
		if truncated {
			return
		}
		n, ok := byID[id]
		if !ok {
			return
		}
		role := n.Role.str()
		// Skip noise nodes but keep walking their children at the same depth.
		if n.Ignored || role == "InlineTextBox" || role == "none" || role == "generic" && n.Name.str() == "" {
			for _, c := range n.ChildIDs {
				walk(c, depth)
			}
			return
		}
		counter++
		uid := fmt.Sprintf("%d_%d", seq, counter)
		if n.BackendDOMNodeID != 0 {
			uids[uid] = n.BackendDOMNodeID
		}
		line := strings.Repeat("  ", depth) + "uid=" + uid + " " + role
		if name := n.Name.str(); name != "" {
			line += fmt.Sprintf(" %q", name)
		}
		if val := n.Value.str(); val != "" {
			line += fmt.Sprintf(" value=%q", val)
		}
		sb.WriteString(line)
		sb.WriteByte('\n')
		if sb.Len() > snapshotCharBudget {
			truncated = true
			return
		}
		for _, c := range n.ChildIDs {
			walk(c, depth+1)
		}
	}

	for _, n := range nodes {
		if !isChild[n.NodeID] {
			walk(n.NodeID, 0)
		}
	}
	if truncated {
		sb.WriteString("... (snapshot truncated at char budget)\n")
	}
	return sb.String(), uids
}
