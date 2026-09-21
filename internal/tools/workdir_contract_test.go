package tools

import (
	"encoding/json"
	"io"
	"log/slog"
	"regexp"
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
	tools := srv.Tools()
	// The floor under every per-tool loop: with no tools registered, each
	// contract would pass without having examined anything.
	if len(tools) == 0 {
		t.Fatal("no tools are registered, so every per-tool contract would pass without examining one")
	}
	return tools
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

// TestEveryRequiredNameIsDeclared is the regression for a tool list that a
// strict client refuses outright. Vertex AI validates `required` against
// `properties` and answers a whole tools/list with
// "schema at top-level requires unspecified property 'work_dir'" — one bad
// schema and the session cannot start at all (2026-09-14, gem-agent).
//
// The existing contract test checks the other direction (declared => required)
// and is blind to this one; JSON Schema itself permits it, so nothing else
// catches it either.
func TestEveryRequiredNameIsDeclared(t *testing.T) {
	for _, tool := range registeredTools(t) {
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatalf("%s: input schema is not valid JSON: %v", tool.Name, err)
		}
		for _, name := range schema.Required {
			if _, ok := schema.Properties[name]; !ok {
				t.Errorf("tool %q requires %q but does not declare it in properties: "+
					"a strict client refuses the whole tool list", tool.Name, name)
			}
		}
	}
}

// declaredProperties returns the argument names a tool's input schema declares.
func declaredProperties(t *testing.T, tool mcpserver.Tool) map[string]bool {
	t.Helper()
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatalf("%s: input schema is not valid JSON: %v", tool.Name, err)
	}
	out := make(map[string]bool, len(schema.Properties))
	for name := range schema.Properties {
		out[name] = true
	}
	return out
}

// identifierPattern picks out the words in prose that read as code names:
// snake_case (tool names, work_dir) and lowerCamelCase (filePath). Plain words
// and capitalised product names such as DevTools do not match.
var identifierPattern = regexp.MustCompile(`^(?:[a-z][a-z0-9]*(?:_[a-z0-9]+)+|[a-z][a-z0-9]*[A-Z][A-Za-z0-9]*)$`)

// identifiersIn returns the code-like words of text, in order.
func identifiersIn(text string) []string {
	var out []string
	for _, word := range regexp.MustCompile(`[A-Za-z0-9_]+`).FindAllString(text, -1) {
		if identifierPattern.MatchString(word) {
			out = append(out, word)
		}
	}
	return out
}

// The initialize `instructions` are the first thing the model reads about this
// server, before any tool list, so the contract has to survive there too: a
// model that reads only this must still learn that the file-writing tools
// require an absolute work_dir. Which tools those are is taken from the
// registered schemas, so a file-producing tool added later fails here until the
// instructions name it.
func TestInstructionsNameTheWorkDirContract(t *testing.T) {
	for _, want := range []string{"work_dir", "absolute", "required", "no default"} {
		if !strings.Contains(Instructions, want) {
			t.Errorf("the initialize instructions do not say %q; a model that reads only "+
				"them will omit or guess an argument the file-writing tools require", want)
		}
	}
	named := map[string]bool{}
	for _, word := range identifiersIn(Instructions) {
		named[word] = true
	}
	declaring := 0
	for _, tool := range registeredTools(t) {
		if !declaredProperties(t, tool)["work_dir"] {
			continue
		}
		declaring++
		if !named[tool.Name] {
			t.Errorf("tool %q requires work_dir but the initialize instructions do not name it", tool.Name)
		}
	}
	if declaring == 0 {
		t.Error("no registered tool declares work_dir, so this contract examined nothing")
	}
}

// Every code name in the instructions must be something the model can call or
// pass. A name that is neither is an instruction the model will follow into an
// "unknown tool" or an "unknown field" — this server decodes arguments strictly.
// The check runs against the registered tool list, never a copy of it.
func TestInstructionsNameOnlyRegisteredTools(t *testing.T) {
	tools := map[string]bool{}
	args := map[string]bool{}
	for _, tool := range registeredTools(t) {
		tools[tool.Name] = true
		for name := range declaredProperties(t, tool) {
			args[name] = true
		}
	}
	toolsNamed := 0
	for _, word := range identifiersIn(Instructions) {
		switch {
		case tools[word]:
			toolsNamed++
		case args[word]:
		default:
			t.Errorf("the initialize instructions mention %q, which is neither a registered tool "+
				"nor an argument any registered tool declares", word)
		}
	}
	if toolsNamed == 0 {
		t.Error("the initialize instructions name no tool, so they do not say where to start")
	}
}

func TestInstructionsNameNoRetiredWorkDirName(t *testing.T) {
	for _, old := range retiredWorkDirNames {
		if strings.Contains(Instructions, old) {
			t.Errorf("the initialize instructions name %q; the name is work_dir", old)
		}
	}
}
