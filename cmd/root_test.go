package cmd

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// The shared homebrew formula template — used by every tool in the org — runs
// `<binary> --version` in its test block. Both spellings must work and must
// print the same string.
func TestVersionFlagAndSubcommandAgree(t *testing.T) {
	run := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if code := Run(args, strings.NewReader(""), &out, io.Discard); code != 0 {
			t.Fatalf("%v: exit code %d", args, code)
		}
		return out.String()
	}

	flag := run("--version")
	sub := run("version")

	if want := Version + "\n"; flag != want {
		t.Errorf("--version printed %q, want %q", flag, want)
	}
	if flag != sub {
		t.Errorf("--version printed %q but `version` printed %q; they must agree", flag, sub)
	}
}

// TestServeListsTools drives the default serve path end-to-end over
// in-memory stdio: initialize must answer with the server identity, and
// tools/list must include the registered tools. No browser is launched —
// the connection is lazy, so this runs without Chrome.
func TestServeListsTools(t *testing.T) {
	stdin := strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	}, "\n") + "\n")

	var stdout, stderr bytes.Buffer
	if code := Run(nil, stdin, &stdout, &stderr); code != 0 {
		t.Fatalf("serve exit code %d, stderr: %s", code, stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, `"name":"chrome-pilot-mcp"`) {
		t.Errorf("initialize response missing server name: %s", out)
	}
	for _, tool := range []string{"list_pages", "new_page", "navigate_page", "take_snapshot", "take_screenshot", "evaluate_script", "wait_for"} {
		if !strings.Contains(out, `"name":"`+tool+`"`) {
			t.Errorf("tools/list missing %s: %s", tool, out)
		}
	}
}

func TestParseViewport(t *testing.T) {
	w, h, err := parseViewport("1280x800")
	if err != nil || w != 1280 || h != 800 {
		t.Errorf("parseViewport(1280x800) = %d,%d,%v", w, h, err)
	}
	for _, bad := range []string{"1280", "x", "0x100", "-1x5", "axb"} {
		if _, _, err := parseViewport(bad); err == nil {
			t.Errorf("parseViewport(%q) should fail", bad)
		}
	}
}

// TestUnknownFlagFails pins the exit code contract: a bad flag must not fall
// through into serve (which would block on stdin).
func TestUnknownFlagFails(t *testing.T) {
	var errBuf bytes.Buffer
	if code := Run([]string{"--no-such-flag"}, strings.NewReader(""), io.Discard, &errBuf); code != 2 {
		t.Errorf("unknown flag: exit code %d, want 2 (stderr: %s)", code, errBuf.String())
	}
}
