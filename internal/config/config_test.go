package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadFullConfig(t *testing.T) {
	path := writeConfig(t, `
# chrome-pilot-mcp configuration
[browser]
headless        = true
channel         = "beta"
executable_path = "/opt/chrome/chrome"   # inline comment
viewport        = "1280x800"
profile         = "work"

[workspace]
root = "/tmp/cp-ws"

[security]
allow_hosts = ["example.com", "*.example.com"]
block_hosts = ["ads.example.com"]
block_local = true
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Headless || cfg.Channel != "beta" || cfg.ExecutablePath != "/opt/chrome/chrome" {
		t.Errorf("browser section = %+v", cfg)
	}
	if cfg.Viewport != "1280x800" || cfg.Profile != "work" {
		t.Errorf("viewport/profile = %q %q", cfg.Viewport, cfg.Profile)
	}
	if cfg.WorkspaceRoot != "/tmp/cp-ws" {
		t.Errorf("workspace root = %q", cfg.WorkspaceRoot)
	}
	if !slices.Equal(cfg.AllowHosts, []string{"example.com", "*.example.com"}) {
		t.Errorf("allow_hosts = %v", cfg.AllowHosts)
	}
	if !slices.Equal(cfg.BlockHosts, []string{"ads.example.com"}) {
		t.Errorf("block_hosts = %v", cfg.BlockHosts)
	}
	if !cfg.BlockLocal {
		t.Errorf("block_local should be true")
	}
}

func TestDefaultsPreservedForAbsentKeys(t *testing.T) {
	path := writeConfig(t, "[browser]\nheadless = true\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Channel != "stable" {
		t.Errorf("channel default lost: %q", cfg.Channel)
	}
	if cfg.AllowHosts != nil {
		t.Errorf("allow_hosts should stay nil, got %v", cfg.AllowHosts)
	}
}

func TestEmptyArrayIsNotNil(t *testing.T) {
	path := writeConfig(t, "[security]\nallow_hosts = []\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.AllowHosts == nil || len(cfg.AllowHosts) != 0 {
		t.Errorf("empty array should decode to empty non-nil slice, got %v", cfg.AllowHosts)
	}
}

// TestRejects pins the "fail loudly" contract: every one of these must be
// an error with a helpful message, never a silent default.
func TestRejects(t *testing.T) {
	tests := []struct {
		name, body, wantSubstr string
	}{
		{"unknown section", "[browsr]\nheadless = true\n", "unknown section"},
		{"unknown key", "[browser]\nheadles = true\n", "unknown key"},
		{"typed bool as string", "[browser]\nheadless = \"yes\"\n", "must be true or false"},
		{"typed string as bool", "[browser]\nchannel = true\n", "must be a string"},
		{"list as string", "[security]\nallow_hosts = \"example.com\"\n", "must be an array"},
		{"key outside section", "headless = true\n", "outside any [section]"},
		{"duplicate key", "[browser]\nchannel = \"beta\"\nchannel = \"dev\"\n", "duplicate key"},
		{"multiline string", "[browser]\nchannel = \"\"\"beta\"\"\"\n", "multi-line string"},
		{"literal string", "[browser]\nchannel = 'beta'\n", "literal string"},
		{"inline table", "[browser]\nchannel = {a = 1}\n", "inline table"},
		{"array of tables", "[[browser]]\n", "array of tables"},
		{"dotted key", "[browser]\na.b = 1\n", "unsupported key"},
		{"float", "[browser]\nchannel = 1.5\n", "unsupported value"},
		{"unterminated string", "[browser]\nchannel = \"beta\n", "unterminated string"},
		{"missing value", "[browser]\nchannel =\n", "missing value"},
		{"unquoted array element", "[security]\nallow_hosts = [example.com]\n", "must be quoted strings"},
		{"no equals", "[browser]\nchannel beta\n", "expected key = value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.body))
			if err == nil {
				t.Fatalf("expected an error")
			}
			if !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantSubstr)
			}
		})
	}
}

// TestErrorsCarryLineNumbers: a typo deep in a file must be locatable.
func TestErrorsCarryLineNumbers(t *testing.T) {
	path := writeConfig(t, "[browser]\nheadless = true\n\n# comment\nchannel = 'beta'\n")
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "line 5") {
		t.Errorf("error should point at line 5, got %v", err)
	}
}

func TestCommentsAndStringsWithHash(t *testing.T) {
	path := writeConfig(t, "[browser]\n# full line comment\nexecutable_path = \"/opt/a#b/chrome\" # trailing\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ExecutablePath != "/opt/a#b/chrome" {
		t.Errorf("hash inside string was stripped: %q", cfg.ExecutablePath)
	}
}

func TestEscapes(t *testing.T) {
	path := writeConfig(t, `[browser]`+"\n"+`executable_path = "/tmp/a\\b\"c"`+"\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ExecutablePath != `/tmp/a\b"c` {
		t.Errorf("escapes = %q", cfg.ExecutablePath)
	}
}

func TestLoadMissingFileIsError(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.toml")); err == nil {
		t.Errorf("missing file should error (explicit --config must fail loudly)")
	}
}

func TestLoadIfExists(t *testing.T) {
	cfg, found, err := LoadIfExists(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil || found {
		t.Errorf("missing default config should be (defaults, false, nil): %v %v", found, err)
	}
	if cfg.Channel != "stable" {
		t.Errorf("defaults not returned: %+v", cfg)
	}

	path := writeConfig(t, "[browser]\nheadless = true\n")
	cfg, found, err = LoadIfExists(path)
	if err != nil || !found || !cfg.Headless {
		t.Errorf("existing config not loaded: %v %v %+v", found, err, cfg)
	}

	// A malformed file at the default path must surface, not fall back.
	bad := writeConfig(t, "[browser]\nchannel = 'x'\n")
	if _, _, err := LoadIfExists(bad); err == nil {
		t.Errorf("malformed default config should error, not silently use defaults")
	}
}

// TestDefaultPathIsNotCwd guards the ADR-0002 decision that ./config.toml
// is never picked up implicitly.
func TestDefaultPathIsNotCwd(t *testing.T) {
	p, err := DefaultPath()
	if err != nil {
		t.Skipf("no user config dir: %v", err)
	}
	if !filepath.IsAbs(p) {
		t.Errorf("default path must be absolute, got %q", p)
	}
	if !strings.Contains(p, filepath.Join("chrome-pilot-mcp", "config.toml")) {
		t.Errorf("default path = %q", p)
	}
	wd, _ := os.Getwd()
	if filepath.Dir(p) == wd {
		t.Errorf("default path must not resolve to the working directory")
	}
}
