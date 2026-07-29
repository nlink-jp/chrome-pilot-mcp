// Package tools implements the MCP tool handlers on top of the CDP client.
//
// Manager owns the browser connection and all per-page state. The browser is
// started lazily on the first tool call that needs it.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nlink-jp/chrome-pilot-mcp/internal/browser"
	"github.com/nlink-jp/chrome-pilot-mcp/internal/cdp"
	"github.com/nlink-jp/chrome-pilot-mcp/internal/toolerr"
	"github.com/nlink-jp/chrome-pilot-mcp/internal/ws"
)

// Config is the server-level configuration from CLI flags.
type Config struct {
	Headless       bool
	Channel        string
	ExecutablePath string
	Attach         string // non-empty → attach instead of launch
	WorkspaceRoot  string
	ViewportWidth  int
	ViewportHeight int
}

// defaultCallTimeout bounds a single CDP call issued by a tool.
const defaultCallTimeout = 30 * time.Second

// pageState is one page target.
type pageState struct {
	targetID  string
	sessionID string // empty until attached
	url       string
	title     string
}

// Manager owns the CDP connection and page/session bookkeeping.
type Manager struct {
	cfg Config

	// connect is injectable for tests; production wiring launches/attaches
	// Chrome and dials its WebSocket endpoint.
	connect func(ctx context.Context) (*cdp.Client, *browser.Browser, error)

	mu       sync.Mutex
	client   *cdp.Client
	br       *browser.Browser
	pages    []*pageState
	selected string // targetID; "" → none

	// pageEnabled tracks sessions where Page/etc. domains are enabled.
	pageEnabled map[string]bool

	snapshotSeq int
	// uids maps a snapshot uid to its backend DOM node id + session,
	// for the input tools (Phase 2).
	uids map[string]uidTarget

	waiterMu sync.Mutex
	waiters  map[waiterKey][]chan json.RawMessage

	wsOnce       sync.Once
	workspaceDir string
	wsErr        error
}

type uidTarget struct {
	backendNodeID int64
	sessionID     string
}

type waiterKey struct {
	sessionID string
	method    string
}

// NewManager creates a Manager with production wiring.
func NewManager(cfg Config) *Manager {
	m := &Manager{cfg: cfg}
	m.connect = m.productionConnect
	m.init()
	return m
}

// newManagerWithConnect is the test constructor.
func newManagerWithConnect(cfg Config, connect func(ctx context.Context) (*cdp.Client, *browser.Browser, error)) *Manager {
	m := &Manager{cfg: cfg, connect: connect}
	m.init()
	return m
}

func (m *Manager) init() {
	m.pageEnabled = make(map[string]bool)
	m.uids = make(map[string]uidTarget)
	m.waiters = make(map[waiterKey][]chan json.RawMessage)
}

func (m *Manager) productionConnect(ctx context.Context) (*cdp.Client, *browser.Browser, error) {
	var (
		br  *browser.Browser
		err error
	)
	if m.cfg.Attach != "" {
		br, err = browser.Attach(ctx, m.cfg.Attach)
	} else {
		br, err = browser.Launch(ctx, browser.Options{
			Headless:       m.cfg.Headless,
			Channel:        m.cfg.Channel,
			ExecutablePath: m.cfg.ExecutablePath,
		})
	}
	if err != nil {
		return nil, nil, toolerr.New(toolerr.CodeBrowserLaunchFailed, err.Error())
	}
	conn, err := ws.Dial(ctx, br.WSURL)
	if err != nil {
		br.Close()
		return nil, nil, toolerr.Newf(toolerr.CodeAttachFailed, "dial %s: %v", br.WSURL, err)
	}
	return cdp.New(conn), br, nil
}

// ensure connects to Chrome on first use.
func (m *Manager) ensure(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client != nil {
		return nil
	}
	client, br, err := m.connect(ctx)
	if err != nil {
		return err
	}
	client.OnEvent(m.dispatchEvent)
	m.client = client
	m.br = br
	return nil
}

// Shutdown closes the connection; a launched Chrome is asked to close
// gracefully, then killed. An attached Chrome is left running.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	client, br := m.client, m.br
	m.client, m.br = nil, nil
	m.mu.Unlock()
	if client == nil {
		return
	}
	if br != nil && br.Launched() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = client.Call(ctx, "", "Browser.close", nil, nil)
		cancel()
	}
	_ = client.Close()
	if br != nil {
		br.Close()
	}
}

// ---- events ----

func (m *Manager) dispatchEvent(ev cdp.Event) {
	key := waiterKey{sessionID: ev.SessionID, method: ev.Method}
	m.waiterMu.Lock()
	chans := m.waiters[key]
	delete(m.waiters, key)
	m.waiterMu.Unlock()
	for _, ch := range chans {
		// Buffered (1); a waiter that gave up just leaves its buffer full.
		select {
		case ch <- ev.Params:
		default:
		}
	}
}

// addWaiter registers a one-shot waiter for (sessionID, method).
func (m *Manager) addWaiter(sessionID, method string) chan json.RawMessage {
	ch := make(chan json.RawMessage, 1)
	key := waiterKey{sessionID: sessionID, method: method}
	m.waiterMu.Lock()
	m.waiters[key] = append(m.waiters[key], ch)
	m.waiterMu.Unlock()
	return ch
}

func (m *Manager) removeWaiter(sessionID, method string, ch chan json.RawMessage) {
	key := waiterKey{sessionID: sessionID, method: method}
	m.waiterMu.Lock()
	defer m.waiterMu.Unlock()
	chans := m.waiters[key]
	for i, c := range chans {
		if c == ch {
			m.waiters[key] = append(chans[:i], chans[i+1:]...)
			break
		}
	}
	if len(m.waiters[key]) == 0 {
		delete(m.waiters, key)
	}
}

// ---- page bookkeeping ----

type targetInfo struct {
	TargetID string `json:"targetId"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	URL      string `json:"url"`
}

// refreshPages re-reads the target list, preserving known ordering.
// Caller must hold m.mu.
func (m *Manager) refreshPagesLocked(ctx context.Context) error {
	var res struct {
		TargetInfos []targetInfo `json:"targetInfos"`
	}
	if err := m.client.Call(ctx, "", "Target.getTargets", nil, &res); err != nil {
		return err
	}
	live := make(map[string]targetInfo)
	for _, ti := range res.TargetInfos {
		if ti.Type == "page" {
			live[ti.TargetID] = ti
		}
	}
	// Keep existing order for surviving targets, refresh their metadata.
	var next []*pageState
	for _, p := range m.pages {
		if ti, ok := live[p.targetID]; ok {
			p.url, p.title = ti.URL, ti.Title
			next = append(next, p)
			delete(live, p.targetID)
		}
	}
	// Append newly discovered targets in the order Chrome reported them.
	for _, ti := range res.TargetInfos {
		if ti.Type != "page" {
			continue
		}
		if _, isNew := live[ti.TargetID]; isNew {
			next = append(next, &pageState{targetID: ti.TargetID, url: ti.URL, title: ti.Title})
		}
	}
	m.pages = next

	// Fix up selection.
	if m.pageByTargetLocked(m.selected) == nil {
		m.selected = ""
		if len(m.pages) > 0 {
			m.selected = m.pages[0].targetID
		}
	}
	return nil
}

func (m *Manager) pageByTargetLocked(targetID string) *pageState {
	if targetID == "" {
		return nil
	}
	for _, p := range m.pages {
		if p.targetID == targetID {
			return p
		}
	}
	return nil
}

// attachPageLocked ensures the page has an attached session with the Page
// domain enabled (and the viewport override applied, if configured).
// Caller must hold m.mu.
func (m *Manager) attachPageLocked(ctx context.Context, p *pageState) error {
	if p.sessionID == "" {
		var res struct {
			SessionID string `json:"sessionId"`
		}
		err := m.client.Call(ctx, "", "Target.attachToTarget",
			map[string]any{"targetId": p.targetID, "flatten": true}, &res)
		if err != nil {
			return err
		}
		p.sessionID = res.SessionID
	}
	if !m.pageEnabled[p.sessionID] {
		if err := m.client.Call(ctx, p.sessionID, "Page.enable", nil, nil); err != nil {
			return err
		}
		if m.cfg.ViewportWidth > 0 && m.cfg.ViewportHeight > 0 {
			err := m.client.Call(ctx, p.sessionID, "Emulation.setDeviceMetricsOverride", map[string]any{
				"width":             m.cfg.ViewportWidth,
				"height":            m.cfg.ViewportHeight,
				"deviceScaleFactor": 1,
				"mobile":            false,
			}, nil)
			if err != nil {
				return err
			}
		}
		m.pageEnabled[p.sessionID] = true
	}
	return nil
}

// selectedPage returns the selected page, attached and ready. It connects
// the browser and refreshes the page list as needed.
func (m *Manager) selectedPage(ctx context.Context) (*pageState, error) {
	if err := m.ensure(ctx); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.refreshPagesLocked(ctx); err != nil {
		return nil, err
	}
	p := m.pageByTargetLocked(m.selected)
	if p == nil {
		return nil, toolerr.New(toolerr.CodePageNotFound, "no pages open; use new_page first")
	}
	if err := m.attachPageLocked(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// ---- workspace ----

// workspace returns the output directory, creating it lazily.
func (m *Manager) workspace() (string, error) {
	m.wsOnce.Do(func() {
		dir := m.cfg.WorkspaceRoot
		if dir == "" {
			d, err := os.MkdirTemp("", "chrome-pilot-mcp-ws-*")
			if err != nil {
				m.wsErr = fmt.Errorf("create workspace: %w", err)
				return
			}
			dir = d
		} else if err := os.MkdirAll(dir, 0o755); err != nil {
			m.wsErr = fmt.Errorf("create workspace: %w", err)
			return
		}
		m.workspaceDir = dir
	})
	return m.workspaceDir, m.wsErr
}

func (m *Manager) workspaceFile(subdir, name string) (string, error) {
	root, err := m.workspace()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, subdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// ---- error mapping ----

// mapErr converts lower-layer errors into structured tool errors.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	var te *toolerr.Error
	if errors.As(err, &te) {
		return err
	}
	var ce *cdp.Error
	if errors.As(err, &ce) {
		return toolerr.New(toolerr.CodeCDPError, ce.Message).WithDetails(map[string]any{
			"method":   ce.Method,
			"cdp_code": ce.Code,
		})
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return toolerr.New(toolerr.CodeTimeout, "operation timed out")
	}
	return err
}

// decodeArgs strictly decodes JSON arguments (unknown fields are errors).
func decodeArgs(raw json.RawMessage, into any) error {
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return toolerr.Newf(toolerr.CodeInvalidArguments, "invalid arguments: %v", err)
	}
	return nil
}
