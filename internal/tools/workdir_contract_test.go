package tools

import (
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/nlink-jp/chrome-pilot-mcp/internal/mcpserver"
	"github.com/nlink-jp/chrome-pilot-mcp/internal/transport"
)

// The work-directory contract (organization ADR-021, project ADR-0005) is a
// rule about every tool, not about one of them. Stated only in prose it gets
// re-decided by whoever adds the next tool — and prose drifts silently
// besides, because nothing compiles it. Both halves are pinned here.

var retiredWorkDirNames = []string{"workspaceRoot", "workspace_root", "workspace_dir"}

func registeredTools(t *testing.T) []mcpserver.Tool {
	t.Helper()
	srv := mcpserver.New("chrome-pilot-mcp", "test",
		transport.NewStdioTransport(strings.NewReader(""), io.Discard),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	f := newFakeChrome(t, "about:blank")
	RegisterAll(srv, newTestManager(t, Config{}, f))
	return srv.Tools()
}

func TestNoToolSchemaOrDescriptionCarriesARetiredWorkDirName(t *testing.T) {
	for _, tool := range registeredTools(t) {
		for _, old := range retiredWorkDirNames {
			if strings.Contains(string(tool.InputSchema), `"`+old+`"`) {
				t.Errorf("tool %q declares %q; the name is work_dir", tool.Name, old)
			}
			if strings.Contains(tool.Description, old) {
				t.Errorf("tool %q describes itself with %q; the name is work_dir", tool.Name, old)
			}
		}
	}
}

// An optional work directory is an invitation to fall back to a server-owned
// default, which is the failure the contract removes.
func TestWorkDirIsRequiredWhereverItIsDeclared(t *testing.T) {
	for _, tool := range registeredTools(t) {
		var schema struct {
			Required   []string                   `json:"required"`
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatalf("%s: input schema is not valid JSON: %v", tool.Name, err)
		}
		if _, declared := schema.Properties["work_dir"]; !declared {
			continue
		}
		found := false
		for _, r := range schema.Required {
			if r == "work_dir" {
				found = true
			}
		}
		if !found {
			t.Errorf("tool %q declares work_dir but does not require it: an optional "+
				"work directory is an invitation to invent a default", tool.Name)
		}
	}
}

// Every tool that hands back a path has to be told where the caller can read.
func TestPathReturningToolsDeclareWorkDir(t *testing.T) {
	// take_screenshot and screencast_start are the two that write a file;
	// screencast_stop writes at stop time using what start was given.
	want := map[string]bool{"take_screenshot": true, "screencast_start": true}
	for _, tool := range registeredTools(t) {
		if !want[tool.Name] {
			continue
		}
		if !strings.Contains(string(tool.InputSchema), `"work_dir"`) {
			t.Errorf("tool %q returns a path but does not declare work_dir", tool.Name)
		}
		delete(want, tool.Name)
	}
	for name := range want {
		t.Errorf("tool %q is not registered any more; update this test deliberately", name)
	}
}
