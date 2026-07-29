package browser

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveProfileEphemeral(t *testing.T) {
	dir, persistent, err := resolveProfile("", "")
	if err != nil || dir != "" || persistent {
		t.Errorf("no options should mean ephemeral: %q %v %v", dir, persistent, err)
	}
}

func TestResolveProfileNamed(t *testing.T) {
	dir, persistent, err := resolveProfile("work", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !persistent {
		t.Errorf("named profile must be persistent")
	}
	root, _ := ProfilesDir()
	if filepath.Dir(dir) != root || filepath.Base(dir) != "work" {
		t.Errorf("named profile dir = %q, want %q/work", dir, root)
	}
}

// TestResolveProfileNameValidation: names go into a filesystem path, so
// separators and traversal must be rejected syntactically.
func TestResolveProfileNameValidation(t *testing.T) {
	for _, bad := range []string{"..", "../escape", "a/b", "a\\b", "with space", "", "sub/../..", "dot.name"} {
		if bad == "" {
			continue // empty means "no profile", covered elsewhere
		}
		if _, _, err := resolveProfile(bad, ""); err == nil {
			t.Errorf("profile name %q should be rejected", bad)
		}
	}
	for _, ok := range []string{"work", "test-1", "my_profile", "ABC123"} {
		if _, _, err := resolveProfile(ok, ""); err != nil {
			t.Errorf("profile name %q should be accepted: %v", ok, err)
		}
	}
}

func TestResolveProfileMutualExclusion(t *testing.T) {
	_, _, err := resolveProfile("work", "/tmp/x")
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("want mutual-exclusion error, got %v", err)
	}
}

func TestResolveProfileExplicitPath(t *testing.T) {
	tmp := t.TempDir()
	dir, persistent, err := resolveProfile("", tmp)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !persistent {
		t.Errorf("explicit path must be persistent (never auto-deleted)")
	}
	if dir != tmp {
		t.Errorf("dir = %q, want %q", dir, tmp)
	}

	// Relative paths are made absolute (Chrome resolves cwd differently).
	rel, _, err := resolveProfile("", "some-relative-dir")
	if err != nil {
		t.Fatalf("resolve relative: %v", err)
	}
	if !filepath.IsAbs(rel) {
		t.Errorf("relative path not made absolute: %q", rel)
	}
}

// TestRefusesRealChromeProfile is the ADR-0003 guardrail: pointing the
// tool at the user's own Chrome data would corrupt their browser.
func TestRefusesRealChromeProfile(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	var real string
	switch runtime.GOOS {
	case "darwin":
		real = filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
	case "linux":
		real = filepath.Join(home, ".config", "google-chrome")
	default:
		t.Skip("platform not covered by this test")
	}

	if _, _, err := resolveProfile("", real); err == nil {
		t.Errorf("real Chrome profile %q must be refused", real)
	}
	// A subdirectory (a specific profile) is refused too.
	if _, _, err := resolveProfile("", filepath.Join(real, "Default")); err == nil {
		t.Errorf("subdirectory of the real profile must be refused")
	}
	// The error must point at the supported alternative.
	_, _, err = resolveProfile("", real)
	if err != nil && !strings.Contains(err.Error(), "--attach") {
		t.Errorf("refusal should suggest --attach, got %v", err)
	}
	// A sibling directory with a similar name is fine.
	if _, _, err := resolveProfile("", real+"-copy-for-testing"); err != nil {
		t.Errorf("similarly-named sibling should be allowed: %v", err)
	}
}

func TestIsRealChromeProfileTable(t *testing.T) {
	getenv := func(k string) string {
		if k == "LOCALAPPDATA" {
			return `C:\Users\magi\AppData\Local`
		}
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}

	darwinReal := filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
	if !isRealChromeProfile(darwinReal, "darwin", getenv) {
		t.Errorf("darwin real profile not detected")
	}
	if isRealChromeProfile(filepath.Join(home, "chrome-pilot-profiles", "work"), "darwin", getenv) {
		t.Errorf("managed profile dir must not be flagged")
	}
	if !isRealChromeProfile(`C:\Users\magi\AppData\Local\Google\Chrome\User Data`, "windows", getenv) {
		t.Errorf("windows real profile not detected")
	}
	if isRealChromeProfile("/tmp/whatever", "linux", getenv) {
		t.Errorf("temp dir must not be flagged")
	}
}

// TestSingletonHint converts Chrome's "profile in use" failure into an
// actionable message instead of a raw stderr dump.
func TestSingletonHint(t *testing.T) {
	tail := "[0729/231500.123:ERROR:process_singleton_posix.cc(351)] Failed to create a ProcessSingleton for your profile directory."
	hint := singletonHint(tail, "/profiles/work")
	if hint == "" || !strings.Contains(hint, "in use") || !strings.Contains(hint, "/profiles/work") {
		t.Errorf("hint = %q", hint)
	}
	if singletonHint("some unrelated chrome noise", "/profiles/work") != "" {
		t.Errorf("unrelated stderr should not produce a singleton hint")
	}
}
