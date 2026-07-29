package browser

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// Profile selection (ADR-0003). Three modes:
//
//	neither set → ephemeral temp dir, removed on Close
//	Profile     → persistent dir under the tool's managed profiles/ area
//	UserDataDir → the given path, used as-is and never removed
//
// Persistent profiles accumulate authenticated state, so the real Chrome
// profile area is refused outright: driving it would corrupt the user's
// browser and collide with Chrome's SingletonLock. `--attach` is the
// supported way to use a real, already-running profile.

var profileNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// ProfilesDir returns the managed directory holding named profiles.
func ProfilesDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("browser: locate user config dir: %w", err)
	}
	return filepath.Join(dir, "chrome-pilot-mcp", "profiles"), nil
}

// resolveProfile turns the profile options into a concrete user-data-dir.
// persistent reports whether the directory must survive Close.
func resolveProfile(profile, userDataDir string) (dir string, persistent bool, err error) {
	switch {
	case profile != "" && userDataDir != "":
		return "", false, fmt.Errorf("browser: --profile and --user-data-dir are mutually exclusive")

	case profile != "":
		if !profileNameRe.MatchString(profile) {
			return "", false, fmt.Errorf("browser: invalid profile name %q (letters, digits, _ and - only)", profile)
		}
		root, err := ProfilesDir()
		if err != nil {
			return "", false, err
		}
		return filepath.Join(root, profile), true, nil

	case userDataDir != "":
		abs, err := filepath.Abs(userDataDir)
		if err != nil {
			return "", false, fmt.Errorf("browser: resolve --user-data-dir: %w", err)
		}
		if isRealChromeProfile(abs, runtime.GOOS, os.Getenv) {
			return "", false, fmt.Errorf(
				"browser: refusing to drive the real Chrome profile at %s "+
					"(it would corrupt your browser state and collide with a running Chrome); "+
					"use --attach to control an already-running Chrome, or --profile for a separate persistent profile", abs)
		}
		return abs, true, nil

	default:
		return "", false, nil // ephemeral
	}
}

// realChromeProfileRoots lists the well-known user-data-dir locations of
// the user's own Chrome/Chromium installs.
func realChromeProfileRoots(goos string, getenv func(string) string) []string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	switch goos {
	case "darwin":
		if home == "" {
			return nil
		}
		base := filepath.Join(home, "Library", "Application Support")
		return []string{
			filepath.Join(base, "Google", "Chrome"),
			filepath.Join(base, "Google", "Chrome Beta"),
			filepath.Join(base, "Google", "Chrome Dev"),
			filepath.Join(base, "Google", "Chrome Canary"),
			filepath.Join(base, "Chromium"),
		}
	case "linux":
		if home == "" {
			return nil
		}
		cfg := filepath.Join(home, ".config")
		return []string{
			filepath.Join(cfg, "google-chrome"),
			filepath.Join(cfg, "google-chrome-beta"),
			filepath.Join(cfg, "google-chrome-unstable"),
			filepath.Join(cfg, "chromium"),
		}
	case "windows":
		var out []string
		for _, env := range []string{"LOCALAPPDATA", "APPDATA"} {
			if base := getenv(env); base != "" {
				out = append(out,
					filepath.Join(base, "Google", "Chrome", "User Data"),
					filepath.Join(base, "Chromium", "User Data"),
				)
			}
		}
		return out
	default:
		return nil
	}
}

// isRealChromeProfile reports whether path is inside (or equal to) one of
// the real Chrome profile roots.
//
// Comparison is done on normalized strings rather than through filepath,
// so a Windows-style path is still recognized when goos is "windows"
// regardless of the separator the host platform uses.
func isRealChromeProfile(path, goos string, getenv func(string) string) bool {
	target := normalizePath(path, goos)
	for _, root := range realChromeProfileRoots(goos, getenv) {
		root = normalizePath(root, goos)
		if root == "" {
			continue
		}
		if target == root || strings.HasPrefix(target+"/", root+"/") {
			return true
		}
	}
	return false
}

// normalizePath makes paths comparable: forward slashes everywhere, no
// trailing separator, and case-folded on the case-insensitive platforms.
func normalizePath(p, goos string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	for len(p) > 1 && strings.HasSuffix(p, "/") {
		p = strings.TrimSuffix(p, "/")
	}
	if goos == "windows" || goos == "darwin" {
		p = strings.ToLower(p)
	}
	return p
}

// singletonHint recognizes the stderr signature of Chrome refusing to
// start because another instance already holds the profile.
func singletonHint(stderrTail, userDataDir string) string {
	lower := strings.ToLower(stderrTail)
	// Patterns must be lowercase: they are matched against lowered text.
	if strings.Contains(lower, "singletonlock") ||
		strings.Contains(lower, "profile appears to be in use") ||
		strings.Contains(lower, "failed to create a processsingleton") ||
		strings.Contains(lower, "process_singleton") ||
		strings.Contains(lower, "existing browser session") {
		return fmt.Sprintf("profile %s is in use by another Chrome instance; close it or use a different --profile", userDataDir)
	}
	return ""
}
