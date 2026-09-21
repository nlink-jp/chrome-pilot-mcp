package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/nlink-jp/chrome-pilot-mcp/internal/browser"
	"github.com/nlink-jp/chrome-pilot-mcp/internal/config"
	"github.com/nlink-jp/chrome-pilot-mcp/internal/toolerr"
)

// The layer these tests observe is resolveWorkDir — the single function every
// file-writing tool calls to turn a `work_dir` argument into a validated
// directory, denied list and all. The resolver's own denial mechanics are
// covered a layer below, in internal/workdir. A test that built its own
// Resolver{Denied: ...} would prove the mechanism and keep passing with the
// wiring deleted, which is the defect this file exists to catch.

// wantDenied asserts that dir is refused with work_dir_denied. It reports an
// accepted directory in those words, because "accepted" is the failure the
// reader needs, not "nil is not a structured error".
func wantDenied(t *testing.T, dir string) {
	t.Helper()
	_, err := resolveWorkDir(context.Background(), dir)
	if err == nil {
		t.Fatalf("resolveWorkDir(%q) accepted the server's own directory; want %s",
			dir, toolerr.CodeWorkDirDenied)
	}
	var te *toolerr.Error
	if !errors.As(err, &te) {
		t.Fatalf("resolveWorkDir(%q) = %v, which is not a structured tool error", dir, err)
	}
	if te.Code != toolerr.CodeWorkDirDenied {
		t.Errorf("resolveWorkDir(%q) = %s, want %s", dir, te.Code, toolerr.CodeWorkDirDenied)
	}
}

// serverDir points the user config directory at one this test owns and
// creates the server's own tree inside it. Nothing here restates the denied
// list — it asks the same expression the server uses.
//
// os.UserConfigDir reads XDG_CONFIG_HOME everywhere but darwin and windows,
// where it is $HOME/Library/Application Support and %AppData%; setting both
// HOME and XDG_CONFIG_HOME covers the two this project builds for.
func serverDir(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if runtime.GOOS == "windows" {
		t.Setenv("AppData", filepath.Join(home, "AppData"))
	}

	dir, err := config.Dir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestWorkDirRefusesServerOwnDir(t *testing.T) {
	wantDenied(t, serverDir(t))
}

// A subdirectory is the obvious way around a check that only compares the
// directory itself — and the subdirectory that matters here is profiles/,
// where the managed browser profiles keep their cookies and sessions.
func TestWorkDirRefusesManagedProfilesDir(t *testing.T) {
	serverDir(t)
	profiles, err := browser.ProfilesDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(profiles, "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	wantDenied(t, profiles)
	wantDenied(t, filepath.Join(profiles, "work"))
}

func TestWorkDirAcceptsOrdinaryDir(t *testing.T) {
	serverDir(t)
	ordinary := t.TempDir()
	got, err := resolveWorkDir(context.Background(), ordinary)
	if err != nil {
		t.Fatalf("resolveWorkDir(%q) = %v, want accepted", ordinary, err)
	}
	want, err := filepath.EvalSymlinks(ordinary)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("resolveWorkDir = %q, want the symlink-resolved %q", got, want)
	}
}
