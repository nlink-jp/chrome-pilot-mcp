package browser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestBuildArgs(t *testing.T) {
	args := buildArgs("/tmp/profile", false)

	for _, want := range []string{
		"--remote-debugging-port=0",
		"--user-data-dir=/tmp/profile",
		"--no-first-run",
	} {
		if !slices.Contains(args, want) {
			t.Errorf("args missing %q: %v", want, args)
		}
	}
	if slices.Contains(args, "--headless=new") {
		t.Errorf("headless flag present without Headless option")
	}
	for _, a := range args {
		if strings.HasPrefix(a, "--remote-debugging-address") {
			t.Errorf("--remote-debugging-address must never be passed (loopback-only posture)")
		}
	}
	if args[len(args)-1] != "about:blank" {
		t.Errorf("last arg should be about:blank, got %q", args[len(args)-1])
	}

	headless := buildArgs("/tmp/profile", true)
	if !slices.Contains(headless, "--headless=new") {
		t.Errorf("headless args missing --headless=new: %v", headless)
	}
}

func TestWSURLRegexp(t *testing.T) {
	line := "DevTools listening on ws://127.0.0.1:54321/devtools/browser/abc-def"
	m := wsURLRe.FindStringSubmatch(line)
	if m == nil || m[1] != "ws://127.0.0.1:54321/devtools/browser/abc-def" {
		t.Errorf("regexp failed on %q: %v", line, m)
	}
	if wsURLRe.MatchString("random stderr noise") {
		t.Errorf("regexp matched noise")
	}
}

func TestExecutableCandidates(t *testing.T) {
	tests := []struct {
		goos, channel string
		wantSubstr    string
	}{
		{"darwin", "stable", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"},
		{"darwin", "canary", "Google Chrome Canary"},
		{"linux", "stable", "google-chrome"},
		{"linux", "beta", "google-chrome-beta"},
	}
	for _, tt := range tests {
		got, err := executableCandidates(tt.goos, tt.channel)
		if err != nil {
			t.Errorf("%s/%s: %v", tt.goos, tt.channel, err)
			continue
		}
		found := false
		for _, c := range got {
			if strings.Contains(c, tt.wantSubstr) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s/%s: no candidate contains %q: %v", tt.goos, tt.channel, tt.wantSubstr, got)
		}
	}

	if _, err := executableCandidates("darwin", "nightly"); err == nil {
		t.Errorf("unknown channel should error")
	}
	if _, err := executableCandidates("plan9", "stable"); err == nil {
		t.Errorf("unsupported OS should error")
	}
}

func TestAttachRefusesNonLoopback(t *testing.T) {
	ctx := context.Background()
	for _, ep := range []string{
		"ws://192.168.1.10:9222/devtools/browser/x",
		"ws://evil.example.com:9222/devtools/browser/x",
		"192.168.1.10:9222",
	} {
		if _, err := Attach(ctx, ep); err == nil || !strings.Contains(err.Error(), "non-loopback") {
			t.Errorf("Attach(%q) should refuse non-loopback, got %v", ep, err)
		}
	}
}

func TestAttachWSURLDirect(t *testing.T) {
	b, err := Attach(context.Background(), "ws://127.0.0.1:9222/devtools/browser/abc")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if b.WSURL != "ws://127.0.0.1:9222/devtools/browser/abc" {
		t.Errorf("WSURL = %q", b.WSURL)
	}
	if b.Launched() {
		t.Errorf("attached browser must not report Launched")
	}
}

func TestAttachViaJSONVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/version" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"Browser":"Chrome/140.0","webSocketDebuggerUrl":"ws://127.0.0.1:54321/devtools/browser/via-json"}`))
	}))
	defer srv.Close()

	hostPort := strings.TrimPrefix(srv.URL, "http://")
	b, err := Attach(context.Background(), hostPort)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if b.WSURL != "ws://127.0.0.1:54321/devtools/browser/via-json" {
		t.Errorf("WSURL = %q", b.WSURL)
	}
}

func TestAttachBadEndpoint(t *testing.T) {
	if _, err := Attach(context.Background(), "not an endpoint"); err == nil {
		t.Errorf("garbage endpoint should error")
	}
}
