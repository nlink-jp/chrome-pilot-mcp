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
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nlink-jp/pathguard"

	"github.com/nlink-jp/chrome-pilot-mcp/internal/browser"
	"github.com/nlink-jp/chrome-pilot-mcp/internal/cdp"
	"github.com/nlink-jp/chrome-pilot-mcp/internal/config"
	"github.com/nlink-jp/chrome-pilot-mcp/internal/toolerr"
	"github.com/nlink-jp/chrome-pilot-mcp/internal/workdir"
	"github.com/nlink-jp/chrome-pilot-mcp/internal/ws"
)

// Config is the server-level configuration (CLI flags merged over
// config.toml — see ADR-0002).
type Config struct {
	Headless       bool
	Channel        string
	ExecutablePath string
	Attach         string // non-empty → attach instead of launch
	ViewportWidth  int
	ViewportHeight int

	// Profile selection (ADR-0003). Empty both → ephemeral temp profile.
	Profile     string
	UserDataDir string

	// Host restrictions (ADR-0001).
	AllowHosts []string
	BlockHosts []string
	BlockLocal bool
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
	cfg    Config
	filter hostFilter
	logger *slog.Logger

	// connect is injectable for tests; production wiring launches/attaches
	// Chrome and dials its WebSocket endpoint.
	connect func(ctx context.Context) (*cdp.Client, *browser.Browser, error)

	mu     sync.Mutex
	client *cdp.Client
	br     *browser.Browser
	// profileDir is the launched Chrome's profile directory, kept from the
	// moment of connecting so protectedPlaces can name it (ADR-0006); "" when
	// attached. It has a lock of its own, placesMu, not m.mu: the Fetch
	// interception judges local files through protectedPlaces while a tool
	// call may hold m.mu waiting on the load it paused (ADR-0008).
	placesMu   sync.Mutex
	profileDir string
	// grants are the work directories each page session was allowed to open
	// local files from (ADR-0008); their own lock, for the same reason.
	grants   fileGrants
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

	// col holds passive event-collector state (console, network, dialogs,
	// screencast frames).
	col collectors
}

// uidTarget locates the DOM node behind a snapshot uid. Accessibility-tree
// nodes carry a backendNodeId; nodes recovered from the DOM (see
// extraInteractiveNodes) carry a nodeId instead. CDP's DOM commands accept
// either, so callers just pass nodeParams().
type uidTarget struct {
	backendNodeID int64
	nodeID        int64
	sessionID     string
}

// nodeParams returns the CDP node reference for this target.
func (t uidTarget) nodeParams() map[string]any {
	if t.backendNodeID != 0 {
		return map[string]any{"backendNodeId": t.backendNodeID}
	}
	return map[string]any{"nodeId": t.nodeID}
}

type waiterKey struct {
	sessionID string
	method    string
}

// NewManager creates a Manager with production wiring.
func NewManager(cfg Config, logger *slog.Logger) *Manager {
	m := &Manager{cfg: cfg, logger: logger}
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
	if m.logger == nil {
		m.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	m.filter = newHostFilter(m.cfg)
	m.pageEnabled = make(map[string]bool)
	m.uids = make(map[string]uidTarget)
	m.waiters = make(map[waiterKey][]chan json.RawMessage)
	m.col.init()
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
			Profile:        m.cfg.Profile,
			UserDataDir:    m.cfg.UserDataDir,
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
	m.placesMu.Lock()
	m.profileDir = br.UserDataDir()
	m.placesMu.Unlock()
	return nil
}

// Shutdown closes the connection; a launched Chrome is asked to close
// gracefully, then killed. An attached Chrome is left running.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	client, br := m.client, m.br
	m.client, m.br = nil, nil
	// profileDir is kept: a throwaway profile is removed only after Chrome
	// exits, and until then it is still this server's to protect.
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
	m.handleCollectorEvent(ev.Method, ev.SessionID, ev.Params)

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
		// Runtime/Network power the console and network collectors; enabling
		// them here means collection starts at first attach.
		if err := m.client.Call(ctx, p.sessionID, "Runtime.enable", nil, nil); err != nil {
			return err
		}
		if err := m.client.Call(ctx, p.sessionID, "Network.enable", nil, nil); err != nil {
			return err
		}
		if err := m.enableFetchGuardLocked(ctx, p.sessionID); err != nil {
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
	return m.selectedPageFor(ctx, false)
}

// selectedPageFor is selectedPage for navigate_page too (navigating): a page
// showing a local file no grant covers may be navigated away from, and used
// for nothing else (ADR-0008).
func (m *Manager) selectedPageFor(ctx context.Context, navigating bool) (*pageState, error) {
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
	// Before attaching: attaching turns the console and network collectors
	// on, and a page showing a local file no grant covers must not feed them.
	if !navigating {
		if err := m.refuseUngrantedLocal(p); err != nil {
			return nil, err
		}
	}
	if err := m.attachPageLocked(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// ---- output ----

// Files are written under the directory the CALLER supplied, through
// writeUnder (confine.go).
//
// The server has no workspace of its own any more. It used to pick one at
// startup — a flag, a config key, else a temp directory — and hand back paths
// into it, which the agent driving the tools usually could not open: the
// runtimes this server is registered with confine their file access to a
// project and a session directory. The information needed to choose lives with
// the caller, so the caller names it on every call (ADR-0005; organization
// ADR-021).

// workDir resolves and validates the caller's work directory for one call:
// the work_dir argument, else the runtime hint in the request's _meta, else
// an error (organization ADR-021 §2). It is refused inside any protected place
// (protectedPlaces), by file identity and by folded name — this disk folds
// case — which is pathguard's judgement (ADR-0007).
//
// Every tool that takes work_dir goes through here, and the resolver is built
// only in resolver, so a tool added later cannot arrive without the places.
func (m *Manager) workDir(ctx context.Context, arg string) (string, error) {
	return m.resolver().Resolve(ctx, arg)
}

// resolver builds the resolver for this moment: the places change while the
// server runs (a browser is launched, a throwaway profile appears), and
// nothing is cached.
func (m *Manager) resolver() workdir.Resolver {
	return workdir.NewResolverFor(m.protectedPlaces()...)
}

// protectedPlaces are the places no work_dir, file argument or write may reach
// (ADR-0006): this server's own directory, the profile of the Chrome it is
// driving, every throwaway profile in the temp directory, and the user's own
// Chrome profiles.
func (m *Manager) protectedPlaces() []pathguard.Place {
	// A relative path — a relative TMPDIR, or this server's own directory
	// under a relative $HOME — is made absolute the way this process uses
	// it; pathguard refuses every call for a place without an absolute path
	// rather than protect nothing. The user's own Chrome roots do not follow
	// this process, so they come from absolute homes only
	// (browser.RealChromeProfileRoots).
	place := func(dir, reason, why string) pathguard.Place {
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
		return pathguard.Place{Path: dir, Kind: pathguard.Protected, Reason: reason, Why: why}
	}
	var out []pathguard.Place
	for _, d := range serverOwnedDirs() {
		out = append(out, place(d, "server_dir",
			"it is inside this server's own directory "+d+" (config.toml and the managed browser profiles)"))
	}
	m.placesMu.Lock()
	profile := m.profileDir
	m.placesMu.Unlock()
	if d := profile; d != "" {
		out = append(out, place(d, "server_dir", "it is inside the profile of the browser this server is driving, "+d))
	}
	// Every throwaway profile in the temp directory, not only this process's:
	// each runtime runs its own server, and a killed one leaves its profile
	// behind, still holding cookies.
	if others, err := filepath.Glob(filepath.Join(os.TempDir(), "chrome-pilot-mcp-profile-*")); err == nil {
		for _, d := range others {
			out = append(out, place(d, "server_dir", "it is inside the throwaway profile of a chrome-pilot-mcp browser, "+d))
		}
	}
	for _, d := range browser.RealChromeProfileRoots() {
		out = append(out, place(d, "browser_profile", "it is inside your own Chrome profile, "+d))
	}
	return out
}

// serverOwnedDirs lists this server's own config and state directories.
//
// There is one tree and it is both: `config.Dir()`. Everything else this
// server produces goes under the caller's `work_dir`, and an ephemeral
// profile lives in a temp directory that is removed on Close.
//
// An operator who points --config at a file somewhere else is not covered:
// that directory is the operator's choice, not this server's own, and
// refusing an arbitrary directory — a project tree, or the process's working
// directory — would deny work directories callers legitimately use.
func serverOwnedDirs() []string {
	dir, err := config.Dir()
	if err != nil || dir == "" {
		return nil
	}
	return []string{dir}
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
