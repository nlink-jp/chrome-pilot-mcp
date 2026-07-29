package tools

import (
	"context"
	"encoding/json"

	"github.com/nlink-jp/chrome-pilot-mcp/internal/toolerr"
)

func (m *Manager) listConsoleMessages(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		PageSize   int      `json:"pageSize"`
		PageIdx    int      `json:"pageIdx"`
		Types      []string `json:"types"`
		SinceMsgID int      `json:"sinceMsgId"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	p, err := m.selectedPage(ctx)
	if err != nil {
		return nil, err
	}

	typeOK := func(t string) bool {
		if len(args.Types) == 0 {
			return true
		}
		for _, want := range args.Types {
			if t == want {
				return true
			}
		}
		return false
	}

	m.col.mu.Lock()
	var msgs []*consoleMsg
	lastID := 0
	for _, msg := range m.col.consoleMsgs {
		if msg.sessionID != p.sessionID {
			continue
		}
		if msg.ID > lastID {
			lastID = msg.ID
		}
		// sinceMsgId lets an agent ask only for what appeared after a
		// previous call, instead of re-reading the whole buffer.
		if msg.ID <= args.SinceMsgID || !typeOK(msg.Type) {
			continue
		}
		msgs = append(msgs, msg)
	}
	m.col.mu.Unlock()

	total := len(msgs)
	msgs = paginate(msgs, args.PageSize, args.PageIdx)
	// lastMsgId is what to pass as sinceMsgId next time, even when the
	// filters matched nothing.
	return map[string]any{"total": total, "messages": msgs, "lastMsgId": lastID}, nil
}

func (m *Manager) getConsoleMessage(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		MsgID *int `json:"msgid"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if args.MsgID == nil {
		return nil, toolerr.New(toolerr.CodeMissingArgument, "msgid is required")
	}
	m.col.mu.Lock()
	defer m.col.mu.Unlock()
	for _, msg := range m.col.consoleMsgs {
		if msg.ID == *args.MsgID {
			out := map[string]any{
				"msgid": msg.ID, "type": msg.Type, "text": msg.Text,
				"timestamp": msg.Timestamp,
			}
			if len(msg.args) > 0 {
				out["args"] = msg.args
			}
			if len(msg.stack) > 0 {
				out["stackTrace"] = msg.stack
			}
			return out, nil
		}
	}
	return nil, toolerr.Newf(toolerr.CodeInvalidArguments, "no console message with msgid %d (it may have been evicted)", *args.MsgID)
}

// paginate applies upstream-style pageSize/pageIdx slicing.
func paginate[T any](items []T, pageSize, pageIdx int) []T {
	if pageSize <= 0 {
		return items
	}
	start := pageIdx * pageSize
	if start >= len(items) {
		return nil
	}
	end := min(start+pageSize, len(items))
	return items[start:end]
}
