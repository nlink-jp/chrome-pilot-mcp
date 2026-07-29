package tools

import (
	"context"
	"encoding/json"

	"github.com/nlink-jp/chrome-pilot-mcp/internal/mcpserver"
)

// toolFunc is the internal handler signature.
type toolFunc func(ctx context.Context, args json.RawMessage) (any, error)

// wrap adapts a toolFunc to the mcpserver handler, mapping lower-layer
// errors to structured tool errors in one place.
func wrap(f toolFunc) mcpserver.ToolHandler {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		out, err := f(ctx, args)
		if err != nil {
			return nil, mapErr(err)
		}
		return out, nil
	}
}

func schema(s string) json.RawMessage { return json.RawMessage(s) }

// RegisterAll registers every implemented tool on the server.
//
// Tool names, parameters, and descriptions follow the upstream
// chrome-devtools-mcp (Apache-2.0) so agents can reuse existing usage
// patterns; see README §Attribution.
func RegisterAll(s *mcpserver.Server, m *Manager) {
	s.RegisterTool(mcpserver.Tool{
		Name:        "list_pages",
		Description: "Lists all open browser pages (tabs) with their index, URL, and title. The selected page is marked.",
		InputSchema: schema(`{"type":"object","properties":{}}`),
	}, wrap(m.listPages))

	s.RegisterTool(mcpserver.Tool{
		Name:        "new_page",
		Description: "Opens a new page (tab), selects it, and optionally navigates it to a URL, waiting for the load event.",
		InputSchema: schema(`{"type":"object","properties":{
			"url":{"type":"string","description":"URL to navigate the new page to. Omit for a blank page."},
			"timeout":{"type":"integer","description":"Navigation timeout in milliseconds. Default 15000."}
		}}`),
	}, wrap(m.newPage))

	s.RegisterTool(mcpserver.Tool{
		Name:        "select_page",
		Description: "Selects the page (tab) at the given index as the target for subsequent tools.",
		InputSchema: schema(`{"type":"object","properties":{
			"pageIdx":{"type":"integer","description":"Index of the page from list_pages."}
		},"required":["pageIdx"]}`),
	}, wrap(m.selectPage))

	s.RegisterTool(mcpserver.Tool{
		Name:        "close_page",
		Description: "Closes the page (tab) at the given index.",
		InputSchema: schema(`{"type":"object","properties":{
			"pageIdx":{"type":"integer","description":"Index of the page from list_pages."}
		},"required":["pageIdx"]}`),
	}, wrap(m.closePage))

	s.RegisterTool(mcpserver.Tool{
		Name:        "navigate_page",
		Description: "Navigates the selected page to a URL and waits for the load event.",
		InputSchema: schema(`{"type":"object","properties":{
			"url":{"type":"string","description":"Destination URL."},
			"timeout":{"type":"integer","description":"Navigation timeout in milliseconds. Default 15000."}
		},"required":["url"]}`),
	}, wrap(m.navigatePage))

	s.RegisterTool(mcpserver.Tool{
		Name:        "wait_for",
		Description: "Waits until the given text appears on the selected page.",
		InputSchema: schema(`{"type":"object","properties":{
			"text":{"type":"string","description":"Text to wait for."},
			"timeout":{"type":"integer","description":"Timeout in milliseconds. Default 15000."}
		},"required":["text"]}`),
	}, wrap(m.waitFor))

	s.RegisterTool(mcpserver.Tool{
		Name:        "evaluate_script",
		Description: "Evaluates a JavaScript function on the selected page and returns its JSON-serializable result. Async functions are awaited.",
		InputSchema: schema(`{"type":"object","properties":{
			"function":{"type":"string","description":"A JavaScript function expression, e.g. \"() => document.title\"."},
			"timeout":{"type":"integer","description":"Timeout in milliseconds. Default 30000."}
		},"required":["function"]}`),
	}, wrap(m.evaluateScript))

	s.RegisterTool(mcpserver.Tool{
		Name:        "take_snapshot",
		Description: "Takes a text snapshot (accessibility tree) of the selected page. Each element gets a uid for use with element-based tools.",
		InputSchema: schema(`{"type":"object","properties":{}}`),
	}, wrap(m.takeSnapshot))

	s.RegisterTool(mcpserver.Tool{
		Name:        "take_screenshot",
		Description: "Takes a screenshot of the selected page, saves it to the workspace, and returns the path (plus the image inline when small enough).",
		InputSchema: schema(`{"type":"object","properties":{
			"format":{"type":"string","enum":["png","jpeg"],"description":"Image format. Default png."},
			"quality":{"type":"integer","description":"JPEG quality 0-100 (jpeg only)."},
			"fullPage":{"type":"boolean","description":"Capture the full scrollable page instead of the viewport."}
		}}`),
	}, wrap(m.takeScreenshot))
}
