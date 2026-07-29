// Package ws is a minimal RFC 6455 WebSocket client for talking CDP to a
// local Chrome. It deliberately supports only what CDP needs — ws:// to a
// loopback host, plaintext, no extensions, no subprotocols — so it can stay
// small and dependency-free (zero-dependency policy, see CLAUDE.md).
package ws

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
)

const acceptGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// Frame opcodes (RFC 6455 §5.2).
const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA
)

// DefaultMaxMessageSize bounds a single assembled message. CDP screenshot
// and snapshot payloads can reach tens of MB; 256MB matches the limit the
// upstream puppeteer transport uses.
const DefaultMaxMessageSize = 256 << 20

// ErrTooLarge is returned when an incoming message exceeds the size limit.
var ErrTooLarge = errors.New("ws: message exceeds size limit")

// Conn is a client WebSocket connection.
//
// Concurrency: WriteMessage/Close are safe from multiple goroutines.
// ReadMessage must be called from a single reader goroutine.
type Conn struct {
	conn       net.Conn
	br         *bufio.Reader
	maxMessage int64

	writeMu   sync.Mutex
	closeOnce sync.Once
	closeErr  error
}

// Dial connects and performs the client handshake. Only ws:// URLs are
// accepted (CDP endpoints are always local and plaintext).
func Dial(ctx context.Context, rawURL string) (*Conn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("ws: parse url: %w", err)
	}
	if u.Scheme != "ws" {
		return nil, fmt.Errorf("ws: unsupported scheme %q (only ws://)", u.Scheme)
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "80")
	}

	var d net.Dialer
	nc, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, fmt.Errorf("ws: dial: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = nc.SetDeadline(deadline)
	}

	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		nc.Close()
		return nil, fmt.Errorf("ws: rand: %w", err)
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)

	path := u.RequestURI()
	if path == "" {
		path = "/"
	}
	req := "GET " + path + " HTTP/1.1\r\n" +
		"Host: " + u.Host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"\r\n"
	if _, err := nc.Write([]byte(req)); err != nil {
		nc.Close()
		return nil, fmt.Errorf("ws: handshake write: %w", err)
	}

	br := bufio.NewReader(nc)
	if err := readHandshakeResponse(br, key); err != nil {
		nc.Close()
		return nil, err
	}

	_ = nc.SetDeadline(time.Time{})
	return &Conn{conn: nc, br: br, maxMessage: DefaultMaxMessageSize}, nil
}

func readHandshakeResponse(br *bufio.Reader, key string) error {
	status, err := br.ReadString('\n')
	if err != nil {
		return fmt.Errorf("ws: handshake read: %w", err)
	}
	if !strings.Contains(status, " 101 ") && !strings.HasSuffix(strings.TrimRight(status, "\r\n"), " 101") {
		return fmt.Errorf("ws: handshake rejected: %s", strings.TrimSpace(status))
	}
	var accept string
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return fmt.Errorf("ws: handshake headers: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(name), "Sec-WebSocket-Accept") {
			accept = strings.TrimSpace(value)
		}
	}
	if accept != acceptKey(key) {
		return fmt.Errorf("ws: bad Sec-WebSocket-Accept")
	}
	return nil
}

func acceptKey(key string) string {
	h := sha1.Sum([]byte(key + acceptGUID))
	return base64.StdEncoding.EncodeToString(h[:])
}

// ReadMessage returns the next complete text/binary message. Control frames
// are handled transparently (ping → pong, close → close echo + io.EOF).
func (c *Conn) ReadMessage() ([]byte, error) {
	var buf []byte
	inMessage := false
	for {
		fin, opcode, payload, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch opcode {
		case opPing:
			if err := c.writeControl(opPong, payload); err != nil {
				return nil, err
			}
		case opPong:
			// Unsolicited pong: ignore.
		case opClose:
			_ = c.writeControl(opClose, payload)
			c.closeTCP()
			return nil, io.EOF
		case opText, opBinary:
			if inMessage {
				return nil, fmt.Errorf("ws: unexpected new data frame mid-message")
			}
			buf = payload
			inMessage = true
			if fin {
				return buf, nil
			}
		case opContinuation:
			if !inMessage {
				return nil, fmt.Errorf("ws: continuation frame without start")
			}
			if int64(len(buf))+int64(len(payload)) > c.maxMessage {
				return nil, ErrTooLarge
			}
			buf = append(buf, payload...)
			if fin {
				return buf, nil
			}
		default:
			return nil, fmt.Errorf("ws: unknown opcode %#x", opcode)
		}
	}
}

func (c *Conn) readFrame() (fin bool, opcode byte, payload []byte, err error) {
	var hdr [2]byte
	if _, err = io.ReadFull(c.br, hdr[:]); err != nil {
		return false, 0, nil, err
	}
	fin = hdr[0]&0x80 != 0
	if hdr[0]&0x70 != 0 {
		return false, 0, nil, fmt.Errorf("ws: unexpected RSV bits (extensions are not negotiated)")
	}
	opcode = hdr[0] & 0x0f
	if hdr[1]&0x80 != 0 {
		return false, 0, nil, fmt.Errorf("ws: server sent masked frame")
	}
	length := int64(hdr[1] & 0x7f)
	switch length {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = int64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		v := binary.BigEndian.Uint64(ext[:])
		if v > uint64(c.maxMessage) {
			return false, 0, nil, ErrTooLarge
		}
		length = int64(v)
	}
	if length > c.maxMessage {
		return false, 0, nil, ErrTooLarge
	}
	payload = make([]byte, length)
	if _, err = io.ReadFull(c.br, payload); err != nil {
		return false, 0, nil, err
	}
	return fin, opcode, payload, nil
}

// WriteMessage sends data as a single masked text frame.
func (c *Conn) WriteMessage(data []byte) error {
	return c.writeFrame(opText, data)
}

func (c *Conn) writeControl(opcode byte, payload []byte) error {
	if len(payload) > 125 {
		payload = payload[:125]
	}
	return c.writeFrame(opcode, payload)
}

func (c *Conn) writeFrame(opcode byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return fmt.Errorf("ws: rand: %w", err)
	}

	header := make([]byte, 0, 14)
	header = append(header, 0x80|opcode)
	switch {
	case len(payload) < 126:
		header = append(header, 0x80|byte(len(payload)))
	case len(payload) <= 0xffff:
		header = append(header, 0x80|126)
		header = binary.BigEndian.AppendUint16(header, uint16(len(payload)))
	default:
		header = append(header, 0x80|127)
		header = binary.BigEndian.AppendUint64(header, uint64(len(payload)))
	}
	header = append(header, mask[:]...)

	masked := make([]byte, len(payload))
	for i, b := range payload {
		masked[i] = b ^ mask[i&3]
	}

	if _, err := c.conn.Write(header); err != nil {
		return err
	}
	_, err := c.conn.Write(masked)
	return err
}

// Close sends a close frame (best effort) and closes the connection.
func (c *Conn) Close() error {
	_ = c.writeControl(opClose, []byte{0x03, 0xe8}) // 1000 normal closure
	c.closeTCP()
	return c.closeErr
}

func (c *Conn) closeTCP() {
	c.closeOnce.Do(func() {
		c.closeErr = c.conn.Close()
	})
}
