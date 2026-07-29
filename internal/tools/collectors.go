package tools

import (
	"encoding/json"
	"strings"
	"sync"
)

// Collector state: passive per-session recordings fed by CDP events.
// Collection starts when a page is attached (Runtime/Network domains are
// enabled there), so only activity after the first tool call is captured.
//
// All state lives behind collectMu — dispatchEvent runs on the CDP read
// loop and must never issue CDP calls or take m.mu.

const (
	maxConsoleMessages = 1000
	maxNetworkRequests = 2000
	maxScreencastFrame = 600
)

type collectors struct {
	mu sync.Mutex

	// dialogs tracks the open JavaScript dialog per session.
	dialogs map[string]*dialogState

	consoleSeq  int
	consoleMsgs []*consoleMsg

	netSeq  int
	netReqs []*netRequest
	netByID map[string]*netRequest // CDP requestId → record

	screencasts map[string]*screencastState
}

type dialogState struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type consoleMsg struct {
	ID        int     `json:"msgid"`
	Type      string  `json:"type"`
	Text      string  `json:"text"`
	Timestamp float64 `json:"timestamp"`
	sessionID string
	args      []json.RawMessage
	stack     json.RawMessage
}

type netRequest struct {
	ID           int     `json:"reqid"`
	URL          string  `json:"url"`
	Method       string  `json:"method"`
	ResourceType string  `json:"resourceType"`
	Status       int     `json:"status"`
	MimeType     string  `json:"mimeType"`
	Failed       string  `json:"failed,omitempty"`
	Finished     bool    `json:"finished"`
	Size         float64 `json:"size"`
	sessionID    string
	requestID    string
	reqHeaders   json.RawMessage
	respHeaders  json.RawMessage
}

type screencastFrame struct {
	data      []byte // decoded image bytes (jpeg)
	timestamp float64
}

type screencastState struct {
	active   bool
	filePath string
	frames   []screencastFrame
	dropped  int
	bytes    int

	// Limits, applied as frames arrive.
	maxFrames     int
	maxBytes      int
	maxDurationMS int
	// limitHit names the limit that first stopped capture, for the report.
	limitHit string
}

func (c *collectors) init() {
	c.dialogs = make(map[string]*dialogState)
	c.netByID = make(map[string]*netRequest)
	c.screencasts = make(map[string]*screencastState)
}

// remoteObjectPreview renders a Runtime.RemoteObject into a short string.
func remoteObjectPreview(raw json.RawMessage) string {
	var obj struct {
		Type        string          `json:"type"`
		Value       json.RawMessage `json:"value"`
		Description string          `json:"description"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return string(raw)
	}
	if obj.Value != nil {
		s := string(obj.Value)
		// Unquote plain strings for readability.
		var str string
		if json.Unmarshal(obj.Value, &str) == nil {
			return str
		}
		return s
	}
	if obj.Description != "" {
		return obj.Description
	}
	return obj.Type
}

// handleCollectorEvent updates collector state for one CDP event.
func (m *Manager) handleCollectorEvent(method, sessionID string, params json.RawMessage) {
	switch method {
	case "Page.javascriptDialogOpening":
		var p struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(params, &p)
		m.col.mu.Lock()
		m.col.dialogs[sessionID] = &dialogState{Type: p.Type, Message: p.Message}
		m.col.mu.Unlock()

	case "Page.javascriptDialogClosed":
		m.col.mu.Lock()
		delete(m.col.dialogs, sessionID)
		m.col.mu.Unlock()

	case "Runtime.consoleAPICalled":
		var p struct {
			Type       string            `json:"type"`
			Args       []json.RawMessage `json:"args"`
			Timestamp  float64           `json:"timestamp"`
			StackTrace json.RawMessage   `json:"stackTrace"`
		}
		_ = json.Unmarshal(params, &p)
		previews := make([]string, 0, len(p.Args))
		for _, a := range p.Args {
			previews = append(previews, remoteObjectPreview(a))
		}
		m.addConsoleMsg(&consoleMsg{
			Type: p.Type, Text: strings.Join(previews, " "),
			Timestamp: p.Timestamp, sessionID: sessionID,
			args: p.Args, stack: p.StackTrace,
		})

	case "Runtime.exceptionThrown":
		var p struct {
			Timestamp        float64 `json:"timestamp"`
			ExceptionDetails struct {
				Text      string          `json:"text"`
				Exception json.RawMessage `json:"exception"`
			} `json:"exceptionDetails"`
		}
		_ = json.Unmarshal(params, &p)
		text := p.ExceptionDetails.Text
		if len(p.ExceptionDetails.Exception) > 0 {
			text += " " + remoteObjectPreview(p.ExceptionDetails.Exception)
		}
		m.addConsoleMsg(&consoleMsg{
			Type: "error", Text: strings.TrimSpace(text),
			Timestamp: p.Timestamp, sessionID: sessionID,
		})

	case "Network.requestWillBeSent":
		var p struct {
			RequestID string `json:"requestId"`
			Type      string `json:"type"`
			Request   struct {
				URL     string          `json:"url"`
				Method  string          `json:"method"`
				Headers json.RawMessage `json:"headers"`
			} `json:"request"`
		}
		_ = json.Unmarshal(params, &p)
		m.col.mu.Lock()
		m.col.netSeq++
		r := &netRequest{
			ID: m.col.netSeq, URL: p.Request.URL, Method: p.Request.Method,
			ResourceType: p.Type, sessionID: sessionID, requestID: p.RequestID,
			reqHeaders: p.Request.Headers,
		}
		m.col.netByID[p.RequestID] = r
		m.col.netReqs = append(m.col.netReqs, r)
		if len(m.col.netReqs) > maxNetworkRequests {
			drop := m.col.netReqs[0]
			delete(m.col.netByID, drop.requestID)
			m.col.netReqs = m.col.netReqs[1:]
		}
		m.col.mu.Unlock()

	case "Network.responseReceived":
		var p struct {
			RequestID string `json:"requestId"`
			Response  struct {
				Status   int             `json:"status"`
				MimeType string          `json:"mimeType"`
				Headers  json.RawMessage `json:"headers"`
			} `json:"response"`
		}
		_ = json.Unmarshal(params, &p)
		m.col.mu.Lock()
		if r := m.col.netByID[p.RequestID]; r != nil {
			r.Status = p.Response.Status
			r.MimeType = p.Response.MimeType
			r.respHeaders = p.Response.Headers
		}
		m.col.mu.Unlock()

	case "Network.loadingFinished":
		var p struct {
			RequestID         string  `json:"requestId"`
			EncodedDataLength float64 `json:"encodedDataLength"`
		}
		_ = json.Unmarshal(params, &p)
		m.col.mu.Lock()
		if r := m.col.netByID[p.RequestID]; r != nil {
			r.Finished = true
			r.Size = p.EncodedDataLength
		}
		m.col.mu.Unlock()

	case "Network.loadingFailed":
		var p struct {
			RequestID string `json:"requestId"`
			ErrorText string `json:"errorText"`
		}
		_ = json.Unmarshal(params, &p)
		m.col.mu.Lock()
		if r := m.col.netByID[p.RequestID]; r != nil {
			r.Finished = true
			r.Failed = p.ErrorText
		}
		m.col.mu.Unlock()

	case "Page.screencastFrame":
		m.handleScreencastFrame(sessionID, params)

	case "Fetch.requestPaused":
		m.handleRequestPaused(sessionID, params)
	}
}

func (m *Manager) addConsoleMsg(msg *consoleMsg) {
	m.col.mu.Lock()
	defer m.col.mu.Unlock()
	m.col.consoleSeq++
	msg.ID = m.col.consoleSeq
	m.col.consoleMsgs = append(m.col.consoleMsgs, msg)
	if len(m.col.consoleMsgs) > maxConsoleMessages {
		m.col.consoleMsgs = m.col.consoleMsgs[1:]
	}
}
