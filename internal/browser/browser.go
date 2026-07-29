// Package browser launches the user's installed Chrome with remote debugging
// enabled, or attaches to an already-running debugging endpoint.
//
// Security posture (see CLAUDE.md): launched Chrome binds its debugging port
// to loopback only (Chrome's default; --remote-debugging-address is never
// passed), uses an ephemeral port and a dedicated user-data-dir, and attach
// targets are refused unless they are loopback. Nothing is ever downloaded.
package browser

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Options configures Launch.
type Options struct {
	Headless       bool
	Channel        string // stable (default) | beta | dev | canary
	ExecutablePath string // explicit binary; overrides Channel
	// Profile selection (ADR-0003): both empty → fresh temp dir removed on
	// Close; Profile → managed persistent profile; UserDataDir → that path.
	Profile     string
	UserDataDir string
}

// Browser is a launched or attached Chrome.
type Browser struct {
	// WSURL is the browser-level CDP WebSocket endpoint.
	WSURL string

	cmd         *exec.Cmd
	userDataDir string
	tempProfile bool
}

// Launched reports whether this process launched Chrome itself (as opposed
// to attaching to an existing one).
func (b *Browser) Launched() bool { return b.cmd != nil }

// wsURLRe matches the endpoint line Chrome prints to stderr.
var wsURLRe = regexp.MustCompile(`DevTools listening on (ws://\S+)`)

// launchTimeout bounds how long we wait for Chrome to print its endpoint.
const launchTimeout = 30 * time.Second

// Launch starts Chrome with remote debugging on an ephemeral loopback port
// and waits for the DevTools endpoint to appear on stderr.
func Launch(ctx context.Context, opts Options) (*Browser, error) {
	exe := opts.ExecutablePath
	if exe == "" {
		var err error
		exe, err = findExecutable(runtime.GOOS, opts.Channel)
		if err != nil {
			return nil, err
		}
	}

	userDataDir, persistent, err := resolveProfile(opts.Profile, opts.UserDataDir)
	if err != nil {
		return nil, err
	}
	tempProfile := !persistent
	if persistent {
		if err := os.MkdirAll(userDataDir, 0o700); err != nil {
			return nil, fmt.Errorf("browser: create profile dir: %w", err)
		}
	} else {
		dir, err := os.MkdirTemp("", "chrome-pilot-mcp-profile-*")
		if err != nil {
			return nil, fmt.Errorf("browser: create profile dir: %w", err)
		}
		userDataDir = dir
	}

	args := buildArgs(userDataDir, opts.Headless)
	cmd := exec.CommandContext(ctx, exe, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("browser: stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		if tempProfile {
			os.RemoveAll(userDataDir)
		}
		return nil, fmt.Errorf("browser: start %s: %w", exe, err)
	}

	wsURL, err := awaitEndpoint(ctx, stderr, userDataDir)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if tempProfile {
			os.RemoveAll(userDataDir)
		}
		return nil, err
	}
	// Keep draining stderr so Chrome never blocks on a full pipe.
	go io.Copy(io.Discard, stderr)

	return &Browser{
		WSURL:       wsURL,
		cmd:         cmd,
		userDataDir: userDataDir,
		tempProfile: tempProfile,
	}, nil
}

func awaitEndpoint(ctx context.Context, stderr io.Reader, userDataDir string) (string, error) {
	type result struct {
		url string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 64*1024), 64*1024)
		var tail []string
		for scanner.Scan() {
			line := scanner.Text()
			if m := wsURLRe.FindStringSubmatch(line); m != nil {
				ch <- result{url: m[1]}
				return
			}
			tail = append(tail, line)
			if len(tail) > 20 {
				tail = tail[1:]
			}
		}
		joined := strings.Join(tail, "\n")
		// A profile already held by another Chrome is the common failure
		// once persistent profiles are in play; say so instead of dumping
		// raw stderr (ADR-0003).
		if hint := singletonHint(joined, userDataDir); hint != "" {
			ch <- result{err: fmt.Errorf("browser: %s", hint)}
			return
		}
		ch <- result{err: fmt.Errorf("browser: Chrome exited before printing a DevTools endpoint; stderr tail:\n%s", joined)}
	}()

	select {
	case r := <-ch:
		return r.url, r.err
	case <-time.After(launchTimeout):
		return "", fmt.Errorf("browser: timed out waiting for DevTools endpoint (%s)", launchTimeout)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// buildArgs returns the Chrome command line. Kept pure for testing.
func buildArgs(userDataDir string, headless bool) []string {
	args := []string{
		// Port 0 = ephemeral; Chrome binds loopback by default and we never
		// pass --remote-debugging-address.
		"--remote-debugging-port=0",
		"--user-data-dir=" + userDataDir,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-search-engine-choice-screen",
		// Reduce background phoning-home in the automation profile.
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-sync",
		"--metrics-recording-only",
	}
	if headless {
		args = append(args, "--headless=new")
	}
	args = append(args, "about:blank")
	return args
}

// findExecutable resolves the Chrome binary for the given OS and channel.
func findExecutable(goos, channel string) (string, error) {
	if channel == "" {
		channel = "stable"
	}
	candidates, err := executableCandidates(goos, channel)
	if err != nil {
		return "", err
	}
	for _, c := range candidates {
		if strings.ContainsRune(c, os.PathSeparator) || goos == "darwin" {
			if _, statErr := os.Stat(c); statErr == nil {
				return c, nil
			}
			continue
		}
		if p, lookErr := exec.LookPath(c); lookErr == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("browser: no Chrome (%s) executable found for %s; use --executable-path", channel, goos)
}

// executableCandidates returns the well-known install locations, most
// preferred first. Kept pure for testing.
func executableCandidates(goos, channel string) ([]string, error) {
	switch goos {
	case "darwin":
		name := map[string]string{
			"stable": "Google Chrome",
			"beta":   "Google Chrome Beta",
			"dev":    "Google Chrome Dev",
			"canary": "Google Chrome Canary",
		}[channel]
		if name == "" {
			return nil, fmt.Errorf("browser: unknown channel %q", channel)
		}
		app := fmt.Sprintf("/Applications/%s.app/Contents/MacOS/%s", name, name)
		home, _ := os.UserHomeDir()
		return []string{app, home + app}, nil
	case "linux":
		name := map[string]string{
			"stable": "google-chrome",
			"beta":   "google-chrome-beta",
			"dev":    "google-chrome-unstable",
			"canary": "google-chrome-unstable",
		}[channel]
		if name == "" {
			return nil, fmt.Errorf("browser: unknown channel %q", channel)
		}
		return []string{name, "chromium", "chromium-browser"}, nil
	case "windows":
		sub := map[string]string{
			"stable": `Google\Chrome`,
			"beta":   `Google\Chrome Beta`,
			"dev":    `Google\Chrome Dev`,
			"canary": `Google\Chrome SxS`,
		}[channel]
		if sub == "" {
			return nil, fmt.Errorf("browser: unknown channel %q", channel)
		}
		var out []string
		for _, env := range []string{"PROGRAMFILES", "PROGRAMFILES(X86)", "LOCALAPPDATA"} {
			if base := os.Getenv(env); base != "" {
				out = append(out, base+`\`+sub+`\Application\chrome.exe`)
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("browser: no Program Files locations found")
		}
		return out, nil
	default:
		return nil, fmt.Errorf("browser: unsupported OS %q", goos)
	}
}

// Attach resolves an existing debugging endpoint. Accepted forms:
//   - "ws://127.0.0.1:9222/devtools/browser/<id>" — used directly
//   - "9222" or "127.0.0.1:9222" — resolved via /json/version
//
// Non-loopback hosts are refused: the debugging protocol is unauthenticated,
// so pointing this tool at a remote host is always a mistake.
func Attach(ctx context.Context, endpoint string) (*Browser, error) {
	if strings.HasPrefix(endpoint, "ws://") {
		u, err := url.Parse(endpoint)
		if err != nil {
			return nil, fmt.Errorf("browser: parse attach endpoint: %w", err)
		}
		if !isLoopbackHost(u.Hostname()) {
			return nil, fmt.Errorf("browser: refusing non-loopback attach target %q", u.Hostname())
		}
		return &Browser{WSURL: endpoint}, nil
	}

	hostPort := endpoint
	if _, err := strconv.Atoi(endpoint); err == nil {
		hostPort = "127.0.0.1:" + endpoint
	}
	host, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		return nil, fmt.Errorf("browser: attach endpoint must be ws://..., a port, or host:port: %w", err)
	}
	if !isLoopbackHost(host) {
		return nil, fmt.Errorf("browser: refusing non-loopback attach target %q", host)
	}

	wsURL, err := fetchWSURL(ctx, hostPort)
	if err != nil {
		return nil, err
	}
	return &Browser{WSURL: wsURL}, nil
}

// fetchWSURL asks the debugging HTTP endpoint for its browser WebSocket URL.
func fetchWSURL(ctx context.Context, hostPort string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+hostPort+"/json/version", nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("browser: query %s/json/version: %w", hostPort, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("browser: %s/json/version: HTTP %d", hostPort, resp.StatusCode)
	}
	var v struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return "", fmt.Errorf("browser: decode /json/version: %w", err)
	}
	if v.WebSocketDebuggerURL == "" {
		return "", fmt.Errorf("browser: /json/version has no webSocketDebuggerUrl")
	}
	return v.WebSocketDebuggerURL, nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Close terminates a launched Chrome (the caller should try Browser.close
// over CDP first for a graceful exit) and removes a temp profile. For
// attached browsers it does nothing.
func (b *Browser) Close() {
	if b.cmd != nil && b.cmd.Process != nil {
		_ = b.cmd.Process.Kill()
		_ = b.cmd.Wait()
	}
	if b.tempProfile && strings.Contains(b.userDataDir, "chrome-pilot-mcp-profile-") {
		_ = os.RemoveAll(b.userDataDir)
	}
}
