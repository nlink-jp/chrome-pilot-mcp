package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Config is the full configuration surface. Field names map 1:1 to CLI
// flags (ADR-0002); every field can also be set in config.toml.
type Config struct {
	// [browser]
	Headless       bool
	Channel        string
	ExecutablePath string
	Attach         string
	Viewport       string
	Profile        string
	UserDataDir    string

	// [workspace]
	WorkspaceRoot string

	// [security]
	AllowHosts []string
	BlockHosts []string
	BlockLocal bool
}

// Default returns the built-in defaults (the behavior of a bare
// `chrome-pilot-mcp` with no flags and no config file).
func Default() Config {
	return Config{Channel: "stable"}
}

// DefaultPath is the only location searched when --config is not given.
// The current directory is deliberately NOT searched: a config can name
// an executable to run and can widen the ADR-0001 host limits, so
// picking one up from whatever directory the server happens to start in
// would be a config-injection channel (ADR-0002).
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "chrome-pilot-mcp", "config.toml"), nil
}

// Load reads and validates a config file into cfg (starting from the
// caller's defaults). Unknown sections/keys and type mismatches are
// errors.
func Load(path string) (Config, error) {
	cfg := Default()
	f, err := os.Open(path)
	if err != nil {
		return cfg, fmt.Errorf("config: %w", err)
	}
	defer f.Close()

	doc, err := parse(f)
	if err != nil {
		return cfg, fmt.Errorf("config %s: %w", path, err)
	}
	if err := apply(&cfg, doc); err != nil {
		return cfg, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

// LoadIfExists loads path when it exists; a missing file yields the
// defaults and found=false. Other errors (permissions, syntax) are
// reported rather than silently ignored.
func LoadIfExists(path string) (cfg Config, found bool, err error) {
	if _, statErr := os.Stat(path); statErr != nil {
		if os.IsNotExist(statErr) {
			return Default(), false, nil
		}
		return Default(), false, fmt.Errorf("config: %w", statErr)
	}
	cfg, err = Load(path)
	return cfg, err == nil, err
}

// knownKeys drives both decoding and the "unknown key" error, so the two
// can never drift apart.
func apply(cfg *Config, doc document) error {
	type binding struct {
		str  *string
		b    *bool
		list *[]string
	}
	schema := map[string]map[string]binding{
		"browser": {
			"headless":        {b: &cfg.Headless},
			"channel":         {str: &cfg.Channel},
			"executable_path": {str: &cfg.ExecutablePath},
			"attach":          {str: &cfg.Attach},
			"viewport":        {str: &cfg.Viewport},
			"profile":         {str: &cfg.Profile},
			"user_data_dir":   {str: &cfg.UserDataDir},
		},
		"workspace": {
			"root": {str: &cfg.WorkspaceRoot},
		},
		"security": {
			"allow_hosts": {list: &cfg.AllowHosts},
			"block_hosts": {list: &cfg.BlockHosts},
			"block_local": {b: &cfg.BlockLocal},
		},
	}

	for section, keys := range doc {
		sec, ok := schema[section]
		if !ok {
			return fmt.Errorf("unknown section [%s] (known: %s)", section, knownNames(schema))
		}
		for key, v := range keys {
			bind, ok := sec[key]
			if !ok {
				return fmt.Errorf("line %d: unknown key %q in [%s] (known: %s)", v.line, key, section, keyNames(sec))
			}
			switch {
			case bind.str != nil:
				if v.kind != kindString {
					return fmt.Errorf("line %d: [%s].%s must be a string", v.line, section, key)
				}
				*bind.str = v.str
			case bind.b != nil:
				if v.kind != kindBool {
					return fmt.Errorf("line %d: [%s].%s must be true or false", v.line, section, key)
				}
				*bind.b = v.b
			case bind.list != nil:
				if v.kind != kindStringList {
					return fmt.Errorf("line %d: [%s].%s must be an array of strings", v.line, section, key)
				}
				*bind.list = v.list
			}
		}
	}
	return nil
}

func knownNames[T any](m map[string]T) string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func keyNames[T any](m map[string]T) string { return knownNames(m) }
