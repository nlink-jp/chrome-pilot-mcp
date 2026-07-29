package tools

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nlink-jp/chrome-pilot-mcp/internal/toolerr"
)

func TestHostAllowedModes(t *testing.T) {
	tests := []struct {
		name  string
		cfg   Config
		host  string
		allow bool
	}{
		// No lists → everything permitted (backward compatible default).
		{"unrestricted", Config{}, "evil.example", true},

		// Allow list → default deny.
		{"allow exact hit", Config{AllowHosts: []string{"example.com"}}, "example.com", true},
		{"allow exact miss", Config{AllowHosts: []string{"example.com"}}, "other.com", false},
		{"allow wildcard sub", Config{AllowHosts: []string{"*.example.com"}}, "api.example.com", true},
		{"allow wildcard deep sub", Config{AllowHosts: []string{"*.example.com"}}, "a.b.example.com", true},
		// The apex is deliberately NOT covered by *.example.com.
		{"allow wildcard excludes apex", Config{AllowHosts: []string{"*.example.com"}}, "example.com", false},
		{"allow apex plus wildcard", Config{AllowHosts: []string{"example.com", "*.example.com"}}, "example.com", true},
		// Suffix-matching must not be fooled by a lookalike domain.
		{"wildcard not fooled by suffix", Config{AllowHosts: []string{"*.example.com"}}, "evilexample.com", false},
		{"wildcard not fooled by longer tld", Config{AllowHosts: []string{"*.example.com"}}, "api.example.com.evil.net", false},
		{"case insensitive", Config{AllowHosts: []string{"Example.COM"}}, "example.com", true},
		{"trailing dot normalized", Config{AllowHosts: []string{"example.com"}}, "example.com.", true},
		{"star matches all", Config{AllowHosts: []string{"*"}}, "anything.test", true},

		// Block list only → denylist mode.
		{"block hit", Config{BlockHosts: []string{"ads.example.com"}}, "ads.example.com", false},
		{"block miss", Config{BlockHosts: []string{"ads.example.com"}}, "example.com", true},
		{"block wildcard", Config{BlockHosts: []string{"*.ads.example"}}, "cdn.ads.example", false},

		// Block wins over allow.
		{"block beats allow", Config{
			AllowHosts: []string{"*.example.com"},
			BlockHosts: []string{"secret.example.com"},
		}, "secret.example.com", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := newHostFilter(tt.cfg).hostAllowed(tt.host); got != tt.allow {
				t.Errorf("hostAllowed(%q) = %v, want %v", tt.host, got, tt.allow)
			}
		})
	}
}

func TestURLAllowed(t *testing.T) {
	restricted := newHostFilter(Config{AllowHosts: []string{"example.com"}})
	blockLocal := newHostFilter(Config{BlockLocal: true})
	open := newHostFilter(Config{})

	tests := []struct {
		name  string
		f     hostFilter
		url   string
		allow bool
	}{
		{"https allowed", restricted, "https://example.com/path?q=1", true},
		{"https denied", restricted, "https://other.com/", false},
		{"port ignored", restricted, "https://example.com:8443/", true},
		{"userinfo does not spoof host", restricted, "https://example.com@evil.test/", false},
		{"file allowed by default", restricted, "file:///tmp/x.html", true},
		{"data allowed by default", restricted, "data:text/html,<h1>hi</h1>", true},
		{"file blocked by block-local", blockLocal, "file:///tmp/x.html", false},
		{"data blocked by block-local", blockLocal, "data:text/html,x", false},
		{"data case insensitive", blockLocal, "DATA:text/html,x", false},
		{"unrestricted lets anything", open, "https://anything.test/", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason := tt.f.urlAllowed(tt.url)
			if got != tt.allow {
				t.Errorf("urlAllowed(%q) = %v (%s), want %v", tt.url, got, reason, tt.allow)
			}
		})
	}
}

func TestFilterActive(t *testing.T) {
	if newHostFilter(Config{}).active() {
		t.Errorf("empty config must be inactive (no Fetch interception)")
	}
	for _, cfg := range []Config{
		{AllowHosts: []string{"x"}}, {BlockHosts: []string{"x"}}, {BlockLocal: true},
	} {
		if !newHostFilter(cfg).active() {
			t.Errorf("%+v should be active", cfg)
		}
	}
}

// TestNavigationBlockedAtToolLayer: the argument check must refuse before
// any CDP navigation is attempted.
func TestNavigationBlockedAtToolLayer(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{AllowHosts: []string{"example.com"}}, f)

	_, err := callTool(t, m.navigatePage, `{"url":"https://evil.test/"}`)
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Code != toolerr.CodeHostNotAllowed {
		t.Fatalf("want host_not_allowed, got %v", err)
	}
	if !strings.Contains(te.Message, "evil.test") {
		t.Errorf("message should name the host: %q", te.Message)
	}
	if n := f.callCount("Page.navigate"); n != 0 {
		t.Errorf("navigation should not reach CDP, got %d Page.navigate calls", n)
	}

	if _, err := callTool(t, m.navigatePage, `{"url":"https://example.com/"}`); err != nil {
		t.Errorf("allowed host should navigate: %v", err)
	}
}

func TestNewPageBlockedAtToolLayer(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{BlockHosts: []string{"evil.test"}}, f)

	_, err := callTool(t, m.newPage, `{"url":"https://evil.test/"}`)
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Code != toolerr.CodeHostNotAllowed {
		t.Fatalf("want host_not_allowed, got %v", err)
	}
	if n := f.callCount("Target.createTarget"); n != 0 {
		t.Errorf("no target should be created for a blocked URL, got %d", n)
	}
}

// TestFetchGuardInstalledOnlyWhenRestricted pins the zero-overhead
// default: Fetch.enable is never called without a configured list.
func TestFetchGuardInstalledOnlyWhenRestricted(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{}, f)
	if _, err := callTool(t, m.listPages, `{}`); err != nil {
		t.Fatal(err)
	}
	// listPages does not attach; take a snapshot to force attach.
	if _, err := callTool(t, m.takeSnapshot, `{}`); err != nil {
		t.Fatal(err)
	}
	if n := f.callCount("Fetch.enable"); n != 0 {
		t.Errorf("unrestricted config must not enable Fetch, got %d calls", n)
	}

	f2 := newFakeChrome(t, "about:blank")
	m2 := newTestManager(t, Config{AllowHosts: []string{"example.com"}}, f2)
	if _, err := callTool(t, m2.takeSnapshot, `{}`); err != nil {
		t.Fatal(err)
	}
	if n := f2.callCount("Fetch.enable"); n != 1 {
		t.Errorf("restricted config should enable Fetch once, got %d", n)
	}
}

// TestFetchGuardDecisions drives the interception handler directly and
// checks the continue/fail answers.
func TestFetchGuardDecisions(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{AllowHosts: []string{"example.com"}}, f)
	if _, err := callTool(t, m.takeSnapshot, `{}`); err != nil {
		t.Fatal(err)
	}

	// A subresource from an allowed host continues.
	f.emit("sess-T1", "Fetch.requestPaused", map[string]any{
		"requestId": "I1", "request": map[string]any{"url": "https://example.com/app.js"},
	})
	waitUntil(t, "continue for allowed host", func() bool { return f.callCount("Fetch.continueRequest") == 1 })

	// One from a non-permitted host is failed with BlockedByClient — this
	// is the path an in-page fetch() to an exfil endpoint takes.
	f.emit("sess-T1", "Fetch.requestPaused", map[string]any{
		"requestId": "I2", "request": map[string]any{"url": "https://exfil.test/collect"},
	})
	waitUntil(t, "fail for blocked host", func() bool { return f.callCount("Fetch.failRequest") == 1 })

	failed := f.callsOf("Fetch.failRequest")[0]
	if failed.params["errorReason"] != "BlockedByClient" || failed.params["requestId"] != "I2" {
		t.Errorf("failRequest params = %v", failed.params)
	}
	if f.callCount("Fetch.continueRequest") != 1 {
		t.Errorf("blocked request must not also be continued")
	}
}

func TestFetchGuardIgnoresMalformedEvent(t *testing.T) {
	f := newFakeChrome(t, "about:blank")
	m := newTestManager(t, Config{AllowHosts: []string{"example.com"}}, f)
	// Must not panic.
	m.handleRequestPaused("sess-T1", json.RawMessage(`not json`))
}
