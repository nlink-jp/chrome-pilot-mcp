package tools

import (
	"net/url"
	"strings"

	"github.com/nlink-jp/chrome-pilot-mcp/internal/toolerr"
)

// Host restrictions (ADR-0001). The lists are fixed at startup and no MCP
// tool can change them, so a prompt-injected agent cannot widen its own
// reach. Enforcement is two-layer: this file provides the decision, the
// tool argument checks give a clear error, and the CDP Fetch interception
// (fetchguard.go) is the boundary that also covers in-page JS.

// hostFilter decides whether a destination is permitted.
type hostFilter struct {
	allow      []string
	block      []string
	blockLocal bool
}

func newHostFilter(cfg Config) hostFilter {
	return hostFilter{allow: cfg.AllowHosts, block: cfg.BlockHosts, blockLocal: cfg.BlockLocal}
}

// active reports whether any restriction is configured. When inactive the
// CDP interception is never installed, so the default path pays nothing.
func (f hostFilter) active() bool {
	return len(f.allow) > 0 || len(f.block) > 0 || f.blockLocal
}

// hostAllowed applies block-then-allow. With no allow list, anything not
// blocked is permitted; with an allow list, the mode is default-deny.
func (f hostFilter) hostAllowed(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" {
		return len(f.allow) == 0
	}
	for _, pat := range f.block {
		if matchHostPattern(pat, host) {
			return false
		}
	}
	if len(f.allow) == 0 {
		return true
	}
	for _, pat := range f.allow {
		if matchHostPattern(pat, host) {
			return true
		}
	}
	return false
}

// urlAllowed applies the filter to a full URL.
//
// http/https are matched by host. file: and data: are governed by
// blockLocal only — they have no host to match, and an allow list is
// about network destinations. Any other scheme (ws:, chrome:, blob:, ...)
// is left to the browser; ADR-0001 documents the WebSocket gap.
func (f hostFilter) urlAllowed(rawURL string) (bool, string) {
	trimmed := strings.TrimSpace(rawURL)
	lower := strings.ToLower(trimmed)

	if strings.HasPrefix(lower, "data:") {
		if f.blockLocal {
			return false, "data: URLs are blocked by --block-local"
		}
		return true, ""
	}

	u, err := url.Parse(trimmed)
	if err != nil {
		return false, "unparsable URL"
	}
	switch strings.ToLower(u.Scheme) {
	case "file":
		if f.blockLocal {
			return false, "file:// URLs are blocked by --block-local"
		}
		return true, ""
	case "http", "https":
		if !f.hostAllowed(u.Hostname()) {
			return false, "host " + u.Hostname() + " is not permitted by the configured allow/block lists"
		}
		return true, ""
	default:
		return true, ""
	}
}

// checkURL is the tool-argument layer: it converts a refusal into the
// structured error agents see.
func (f hostFilter) checkURL(rawURL string) error {
	if !f.active() {
		return nil
	}
	if ok, reason := f.urlAllowed(rawURL); !ok {
		return toolerr.New(toolerr.CodeHostNotAllowed, reason).WithDetails(map[string]any{
			"url": rawURL,
		})
	}
	return nil
}

// matchHostPattern matches a host against one pattern.
//
// Patterns are case-insensitive and either an exact hostname or a leading
// wildcard label: "*.example.com" matches sub.example.com and
// a.b.example.com, but NOT example.com itself — security configuration
// favors explicitness, so the apex must be listed separately. A bare "*"
// matches everything.
func matchHostPattern(pattern, host string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	switch {
	case pattern == "":
		return false
	case pattern == "*":
		return true
	case strings.HasPrefix(pattern, "*."):
		suffix := pattern[1:] // ".example.com"
		return strings.HasSuffix(host, suffix) && len(host) > len(suffix)
	default:
		return host == pattern
	}
}
