package ws

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---- minimal server side (test harness only) ----

type serverConn struct {
	conn net.Conn
	br   *bufio.Reader
}

// startServer runs a hijacking WebSocket server and calls handler with the
// accepted connection. Returns the ws:// URL.
func startServer(t *testing.T, handler func(sc *serverConn)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			t.Errorf("missing Upgrade header")
		}
		key := r.Header.Get("Sec-WebSocket-Key")
		if key == "" {
			t.Errorf("missing Sec-WebSocket-Key")
		}
		h := sha1.Sum([]byte(key + acceptGUID))
		accept := base64.StdEncoding.EncodeToString(h[:])

		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatalf("response writer is not a Hijacker")
		}
		conn, brw, err := hj.Hijack()
		if err != nil {
			t.Fatalf("hijack: %v", err)
		}
		resp := "HTTP/1.1 101 Switching Protocols\r\n" +
			"Upgrade: websocket\r\n" +
			"Connection: Upgrade\r\n" +
			"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
		if _, err := conn.Write([]byte(resp)); err != nil {
			t.Errorf("write 101: %v", err)
			conn.Close()
			return
		}
		handler(&serverConn{conn: conn, br: brw.Reader})
	}))
	t.Cleanup(srv.Close)
	return "ws://" + srv.Listener.Addr().String() + "/session"
}

// readMessage reads one complete (possibly fragmented) client message,
// verifying that client frames are masked.
func (sc *serverConn) readMessage(t *testing.T) []byte {
	t.Helper()
	var out []byte
	for {
		var hdr [2]byte
		if _, err := io.ReadFull(sc.br, hdr[:]); err != nil {
			t.Fatalf("server read header: %v", err)
		}
		fin := hdr[0]&0x80 != 0
		opcode := hdr[0] & 0x0f
		if hdr[1]&0x80 == 0 {
			t.Fatalf("client frame not masked (RFC 6455 violation)")
		}
		length := int64(hdr[1] & 0x7f)
		switch length {
		case 126:
			var ext [2]byte
			io.ReadFull(sc.br, ext[:])
			length = int64(binary.BigEndian.Uint16(ext[:]))
		case 127:
			var ext [8]byte
			io.ReadFull(sc.br, ext[:])
			length = int64(binary.BigEndian.Uint64(ext[:]))
		}
		var mask [4]byte
		if _, err := io.ReadFull(sc.br, mask[:]); err != nil {
			t.Fatalf("server read mask: %v", err)
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(sc.br, payload); err != nil {
			t.Fatalf("server read payload: %v", err)
		}
		for i := range payload {
			payload[i] ^= mask[i&3]
		}
		switch opcode {
		case opClose:
			return nil
		case opPong:
			return payload // tests that expect a pong read it directly
		case opPing:
			continue
		default:
			out = append(out, payload...)
			if fin {
				return out
			}
		}
	}
}

// writeFrame writes one unmasked server frame.
func (sc *serverConn) writeFrame(t *testing.T, fin bool, opcode byte, payload []byte) {
	t.Helper()
	var b0 byte = opcode
	if fin {
		b0 |= 0x80
	}
	header := []byte{b0}
	switch {
	case len(payload) < 126:
		header = append(header, byte(len(payload)))
	case len(payload) <= 0xffff:
		header = append(header, 126)
		header = binary.BigEndian.AppendUint16(header, uint16(len(payload)))
	default:
		header = append(header, 127)
		header = binary.BigEndian.AppendUint64(header, uint64(len(payload)))
	}
	if _, err := sc.conn.Write(append(header, payload...)); err != nil {
		t.Errorf("server write: %v", err)
	}
}

// ---- tests ----

func dialT(t *testing.T, url string) *Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, url)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestEchoRoundTrip(t *testing.T) {
	url := startServer(t, func(sc *serverConn) {
		msg := sc.readMessage(t)
		sc.writeFrame(t, true, opText, msg)
	})
	c := dialT(t, url)

	want := []byte(`{"id":1,"method":"Target.getTargets"}`)
	if err := c.WriteMessage(want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("echo mismatch: got %q want %q", got, want)
	}
}

// TestLargeMessage exercises both extended-length encodings (16-bit on the
// write path via a 70KB payload, and the server echoing it back in one
// frame).
func TestLargeMessage(t *testing.T) {
	url := startServer(t, func(sc *serverConn) {
		msg := sc.readMessage(t)
		sc.writeFrame(t, true, opBinary, msg)
	})
	c := dialT(t, url)

	want := bytes.Repeat([]byte("x"), 70_000)
	if err := c.WriteMessage(want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("large echo mismatch: got %d bytes, want %d", len(got), len(want))
	}
}

// TestVeryLargeMessage covers the 64-bit length path (>65535 payload).
func TestVeryLargeMessage(t *testing.T) {
	url := startServer(t, func(sc *serverConn) {
		sc.writeFrame(t, true, opText, bytes.Repeat([]byte("y"), 100_000))
	})
	c := dialT(t, url)

	got, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 100_000 {
		t.Errorf("got %d bytes, want 100000", len(got))
	}
}

func TestFragmentedMessage(t *testing.T) {
	url := startServer(t, func(sc *serverConn) {
		sc.writeFrame(t, false, opText, []byte("hel"))
		sc.writeFrame(t, false, opContinuation, []byte("lo "))
		sc.writeFrame(t, true, opContinuation, []byte("world"))
	})
	c := dialT(t, url)

	got, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("got %q", got)
	}
}

// TestPingDuringFragmentation: control frames may be interleaved between
// fragments; the client must answer the ping and still assemble the message.
func TestPingDuringFragmentation(t *testing.T) {
	pong := make(chan []byte, 1)
	url := startServer(t, func(sc *serverConn) {
		sc.writeFrame(t, false, opText, []byte("ab"))
		sc.writeFrame(t, true, opPing, []byte("tick"))
		pong <- sc.readMessage(t) // pong comes back masked
		sc.writeFrame(t, true, opContinuation, []byte("cd"))
	})
	c := dialT(t, url)

	got, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "abcd" {
		t.Errorf("got %q", got)
	}
	select {
	case p := <-pong:
		if string(p) != "tick" {
			t.Errorf("pong payload %q, want tick", p)
		}
	case <-time.After(2 * time.Second):
		t.Errorf("no pong received")
	}
}

func TestServerClose(t *testing.T) {
	url := startServer(t, func(sc *serverConn) {
		sc.writeFrame(t, true, opClose, []byte{0x03, 0xe8})
	})
	c := dialT(t, url)

	_, err := c.ReadMessage()
	if !errors.Is(err, io.EOF) {
		t.Errorf("want io.EOF on server close, got %v", err)
	}
}

func TestRejectNonWSScheme(t *testing.T) {
	for _, u := range []string{"wss://127.0.0.1:1/x", "http://127.0.0.1:1/x"} {
		if _, err := Dial(context.Background(), u); err == nil {
			t.Errorf("Dial(%q) should fail", u)
		}
	}
}

func TestBadAcceptKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj := w.(http.Hijacker)
		conn, _, _ := hj.Hijack()
		fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: bogus\r\n\r\n")
		conn.Close()
	}))
	defer srv.Close()

	_, err := Dial(context.Background(), "ws://"+srv.Listener.Addr().String()+"/x")
	if err == nil || !strings.Contains(err.Error(), "Sec-WebSocket-Accept") {
		t.Errorf("want accept-key error, got %v", err)
	}
}

func TestHandshakeRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := Dial(context.Background(), "ws://"+srv.Listener.Addr().String()+"/x")
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Errorf("want handshake-rejected error, got %v", err)
	}
}
