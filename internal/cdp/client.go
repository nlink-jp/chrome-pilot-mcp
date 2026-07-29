// Package cdp implements a minimal Chrome DevTools Protocol client on top of
// a message-oriented connection (internal/ws in production, a fake in tests).
//
// It provides request/response correlation by id, session routing via the
// flattened protocol (sessionId on every message), and event fan-out to
// registered handlers. It knows nothing about specific CDP domains — that
// knowledge lives in the layers above.
package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// Conn is the message transport the client runs on.
type Conn interface {
	ReadMessage() ([]byte, error)
	WriteMessage([]byte) error
	Close() error
}

// Error is a CDP protocol-level error response.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
	// Method is the request method that failed (filled in by the client).
	Method string `json:"-"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("cdp: %s: %s (code %d)", e.Method, e.Message, e.Code)
}

// ErrClosed is returned by Call after the connection is gone.
var ErrClosed = errors.New("cdp: connection closed")

// Event is a CDP event notification.
type Event struct {
	Method    string
	Params    json.RawMessage
	SessionID string
}

// outbound is a request message.
type outbound struct {
	ID        int64  `json:"id"`
	Method    string `json:"method"`
	Params    any    `json:"params,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
}

// inbound is a response or event message.
type inbound struct {
	ID        int64           `json:"id"`
	Method    string          `json:"method"`
	Params    json.RawMessage `json:"params"`
	SessionID string          `json:"sessionId"`
	Result    json.RawMessage `json:"result"`
	Error     *Error          `json:"error"`
}

type pendingCall struct {
	method string
	ch     chan inbound
}

// Client is a CDP client. Create with New; Close tears down the transport
// and unblocks all in-flight calls.
type Client struct {
	conn Conn

	mu      sync.Mutex
	nextID  int64
	pending map[int64]pendingCall
	closed  bool
	readErr error

	handlerMu sync.RWMutex
	handlers  []func(Event)

	done chan struct{}
}

// New wraps conn and starts the read loop.
func New(conn Conn) *Client {
	c := &Client{
		conn:    conn,
		pending: make(map[int64]pendingCall),
		done:    make(chan struct{}),
	}
	go c.readLoop()
	return c
}

// OnEvent registers a handler invoked (synchronously, from the read loop)
// for every CDP event. Handlers must be fast and must not call Call inline;
// hand off to a channel or goroutine for anything heavier.
func (c *Client) OnEvent(h func(Event)) {
	c.handlerMu.Lock()
	defer c.handlerMu.Unlock()
	c.handlers = append(c.handlers, h)
}

// Call sends method with params (may be nil) and decodes the result into
// result (may be nil). sessionID targets an attached session; empty means
// the browser-level session.
func (c *Client) Call(ctx context.Context, sessionID, method string, params, result any) error {
	c.mu.Lock()
	if c.closed {
		err := c.readErr
		c.mu.Unlock()
		if err == nil {
			err = ErrClosed
		}
		return err
	}
	c.nextID++
	id := c.nextID
	ch := make(chan inbound, 1)
	c.pending[id] = pendingCall{method: method, ch: ch}
	c.mu.Unlock()

	payload, err := json.Marshal(outbound{ID: id, Method: method, Params: params, SessionID: sessionID})
	if err != nil {
		c.dropPending(id)
		return fmt.Errorf("cdp: marshal %s: %w", method, err)
	}
	if err := c.conn.WriteMessage(payload); err != nil {
		c.dropPending(id)
		return fmt.Errorf("cdp: write %s: %w", method, err)
	}

	select {
	case <-ctx.Done():
		c.dropPending(id)
		return ctx.Err()
	case <-c.done:
		c.mu.Lock()
		err := c.readErr
		c.mu.Unlock()
		if err == nil {
			err = ErrClosed
		}
		return err
	case resp, ok := <-ch:
		if !ok {
			// The read loop died and closed the channel.
			return ErrClosed
		}
		if resp.Error != nil {
			resp.Error.Method = method
			return resp.Error
		}
		if result != nil {
			if err := json.Unmarshal(resp.Result, result); err != nil {
				return fmt.Errorf("cdp: decode %s result: %w", method, err)
			}
		}
		return nil
	}
}

// Close tears down the transport. Safe to call multiple times.
func (c *Client) Close() error {
	err := c.conn.Close()
	<-c.done // wait for the read loop to finish so pending calls are drained
	return err
}

func (c *Client) dropPending(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *Client) readLoop() {
	defer close(c.done)
	for {
		data, err := c.conn.ReadMessage()
		if err != nil {
			c.mu.Lock()
			c.closed = true
			c.readErr = ErrClosed
			// Unblock every in-flight Call.
			for id, p := range c.pending {
				close(p.ch)
				delete(c.pending, id)
			}
			c.mu.Unlock()
			return
		}
		var msg inbound
		if err := json.Unmarshal(data, &msg); err != nil {
			// A malformed frame from Chrome is unexpected; skip it rather
			// than killing the connection.
			continue
		}
		if msg.ID != 0 {
			c.mu.Lock()
			p, ok := c.pending[msg.ID]
			if ok {
				delete(c.pending, msg.ID)
			}
			c.mu.Unlock()
			if ok {
				p.ch <- msg
			}
			continue
		}
		if msg.Method != "" {
			ev := Event{Method: msg.Method, Params: msg.Params, SessionID: msg.SessionID}
			c.handlerMu.RLock()
			handlers := c.handlers
			c.handlerMu.RUnlock()
			for _, h := range handlers {
				h(ev)
			}
		}
	}
}
