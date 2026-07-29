package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"
)

// fakeConn is an in-memory Conn: WriteMessage hands the request to a
// scripted responder; ReadMessage delivers whatever the responder (or the
// test, via inject) queues up.
type fakeConn struct {
	respond func(req map[string]any) [][]byte // may return multiple frames

	mu     sync.Mutex
	queue  chan []byte
	closed bool
}

func newFakeConn(respond func(req map[string]any) [][]byte) *fakeConn {
	return &fakeConn{respond: respond, queue: make(chan []byte, 64)}
}

func (f *fakeConn) inject(frame string) { f.queue <- []byte(frame) }

func (f *fakeConn) ReadMessage() ([]byte, error) {
	data, ok := <-f.queue
	if !ok {
		return nil, io.EOF
	}
	return data, nil
}

func (f *fakeConn) WriteMessage(data []byte) error {
	var req map[string]any
	if err := json.Unmarshal(data, &req); err != nil {
		return err
	}
	if f.respond != nil {
		for _, frame := range f.respond(req) {
			f.queue <- frame
		}
	}
	return nil
}

func (f *fakeConn) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		close(f.queue)
	}
	return nil
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestCallRoundTrip(t *testing.T) {
	conn := newFakeConn(func(req map[string]any) [][]byte {
		if req["method"] != "Target.getVersion" {
			t.Errorf("unexpected method %v", req["method"])
		}
		id := int64(req["id"].(float64))
		return [][]byte{fmt.Appendf(nil, `{"id":%d,"result":{"product":"Chrome/140.0"}}`, id)}
	})
	c := New(conn)
	defer c.Close()

	var res struct {
		Product string `json:"product"`
	}
	if err := c.Call(ctxT(t), "", "Target.getVersion", nil, &res); err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.Product != "Chrome/140.0" {
		t.Errorf("product = %q", res.Product)
	}
}

func TestCallSendsSessionID(t *testing.T) {
	conn := newFakeConn(func(req map[string]any) [][]byte {
		if req["sessionId"] != "sess-1" {
			t.Errorf("sessionId = %v, want sess-1", req["sessionId"])
		}
		return [][]byte{fmt.Appendf(nil, `{"id":%d,"sessionId":"sess-1","result":{}}`, int64(req["id"].(float64)))}
	})
	c := New(conn)
	defer c.Close()

	if err := c.Call(ctxT(t), "sess-1", "Page.enable", nil, nil); err != nil {
		t.Fatalf("call: %v", err)
	}
}

func TestCallProtocolError(t *testing.T) {
	conn := newFakeConn(func(req map[string]any) [][]byte {
		return [][]byte{fmt.Appendf(nil, `{"id":%d,"error":{"code":-32000,"message":"target closed"}}`, int64(req["id"].(float64)))}
	})
	c := New(conn)
	defer c.Close()

	err := c.Call(ctxT(t), "", "Page.navigate", map[string]any{"url": "about:blank"}, nil)
	var ce *Error
	if !errors.As(err, &ce) {
		t.Fatalf("want *cdp.Error, got %v", err)
	}
	if ce.Code != -32000 || ce.Message != "target closed" || ce.Method != "Page.navigate" {
		t.Errorf("error = %+v", ce)
	}
}

// TestOutOfOrderResponses: responses may arrive in any order; each call must
// receive its own.
func TestOutOfOrderResponses(t *testing.T) {
	conn := newFakeConn(nil)
	c := New(conn)
	defer c.Close()

	type result struct {
		V string `json:"v"`
	}
	// Start the calls one at a time so id assignment is deterministic
	// (call i+1 gets id i+1), then answer in reverse order.
	waitPending := func(n int) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for {
			c.mu.Lock()
			got := len(c.pending)
			c.mu.Unlock()
			if got == n {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("pending never reached %d", n)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	var wg sync.WaitGroup
	results := make([]result, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = c.Call(ctxT(t), "", fmt.Sprintf("m%d", i+1), nil, &results[i])
		}()
		waitPending(i + 1)
	}
	conn.inject(`{"id":2,"result":{"v":"second"}}`)
	conn.inject(`{"id":1,"result":{"v":"first"}}`)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if results[0].V != "first" || results[1].V != "second" {
		t.Errorf("results = %+v", results)
	}
}

func TestEventDispatch(t *testing.T) {
	conn := newFakeConn(nil)
	c := New(conn)
	defer c.Close()

	got := make(chan Event, 1)
	c.OnEvent(func(ev Event) { got <- ev })

	conn.inject(`{"method":"Page.loadEventFired","params":{"timestamp":42},"sessionId":"sess-9"}`)

	select {
	case ev := <-got:
		if ev.Method != "Page.loadEventFired" || ev.SessionID != "sess-9" {
			t.Errorf("event = %+v", ev)
		}
		if string(ev.Params) != `{"timestamp":42}` {
			t.Errorf("params = %s", ev.Params)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event not dispatched")
	}
}

// TestCloseUnblocksPending: closing the transport must fail in-flight calls
// instead of hanging them.
func TestCloseUnblocksPending(t *testing.T) {
	conn := newFakeConn(nil) // never responds
	c := New(conn)

	done := make(chan error, 1)
	go func() {
		done <- c.Call(context.Background(), "", "Page.navigate", nil, nil)
	}()
	time.Sleep(20 * time.Millisecond)
	c.Close()

	select {
	case err := <-done:
		if !errors.Is(err, ErrClosed) {
			t.Errorf("want ErrClosed, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pending call not unblocked by Close")
	}
}

// TestCallAfterClose returns ErrClosed immediately.
func TestCallAfterClose(t *testing.T) {
	conn := newFakeConn(nil)
	c := New(conn)
	c.Close()

	if err := c.Call(ctxT(t), "", "Page.enable", nil, nil); !errors.Is(err, ErrClosed) {
		t.Errorf("want ErrClosed, got %v", err)
	}
}
