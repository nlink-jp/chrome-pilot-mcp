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
		Description: "Waits until text appears on the selected page, or until a CSS selector reaches a state (visible, hidden, present, absent). Use the selector form to wait for a spinner or overlay to go away.",
		InputSchema: schema(`{"type":"object","properties":{
			"text":{"type":"string","description":"Text to wait for. Mutually exclusive with selector."},
			"selector":{"type":"string","description":"CSS selector to wait on, e.g. \".spinner\"."},
			"state":{"type":"string","enum":["visible","hidden","present","absent"],"description":"State the selector must reach. Default visible."},
			"timeout":{"type":"integer","description":"Timeout in milliseconds. Default 15000."}
		}}`),
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

	registerInputTools(s, m)
	registerObservabilityTools(s, m)
}

const includeSnapshotProp = `"includeSnapshot":{"type":"boolean","description":"Whether to include a fresh snapshot in the response. Default is false."}`

func registerInputTools(s *mcpserver.Server, m *Manager) {
	s.RegisterTool(mcpserver.Tool{
		Name:        "click",
		Description: "Clicks on the provided element.",
		InputSchema: schema(`{"type":"object","properties":{
			"uid":{"type":"string","description":"The uid of an element on the page from the page content snapshot."},
			"dblClick":{"type":"boolean","description":"Set to true for double clicks. Default is false."},
			` + includeSnapshotProp + `
		},"required":["uid"]}`),
	}, wrap(m.click))

	s.RegisterTool(mcpserver.Tool{
		Name:        "click_at",
		Description: "Clicks at the given viewport coordinates.",
		InputSchema: schema(`{"type":"object","properties":{
			"x":{"type":"number","description":"The x coordinate."},
			"y":{"type":"number","description":"The y coordinate."},
			"dblClick":{"type":"boolean","description":"Set to true for double clicks. Default is false."},
			` + includeSnapshotProp + `
		},"required":["x","y"]}`),
	}, wrap(m.clickAt))

	s.RegisterTool(mcpserver.Tool{
		Name:        "hover",
		Description: "Hovers over the provided element.",
		InputSchema: schema(`{"type":"object","properties":{
			"uid":{"type":"string","description":"The uid of an element on the page from the page content snapshot."},
			` + includeSnapshotProp + `
		},"required":["uid"]}`),
	}, wrap(m.hover))

	s.RegisterTool(mcpserver.Tool{
		Name:        "drag",
		Description: "Drags one element onto another with a mouse press, move and release. Chrome turns that sequence into a native drag, so HTML5 draggable elements and ondrop handlers do fire; sliders and other mouse-tracking UIs work directly. Drag-and-drop built on raw pointer events with its own thresholds may need finer steps than this single move.",
		InputSchema: schema(`{"type":"object","properties":{
			"from_uid":{"type":"string","description":"The uid of the element to drag."},
			"to_uid":{"type":"string","description":"The uid of the element to drop into."},
			` + includeSnapshotProp + `
		},"required":["from_uid","to_uid"]}`),
	}, wrap(m.drag))

	s.RegisterTool(mcpserver.Tool{
		Name:        "fill",
		Description: "Fills a value into an input, textarea, select, checkbox/radio (\"true\"/\"false\"), or contenteditable element.",
		InputSchema: schema(`{"type":"object","properties":{
			"uid":{"type":"string","description":"The uid of an element on the page from the page content snapshot."},
			"value":{"type":"string","description":"The value to fill in. \"true\" or \"false\" for checkboxes and toggles."},
			` + includeSnapshotProp + `
		},"required":["uid","value"]}`),
	}, wrap(m.fill))

	s.RegisterTool(mcpserver.Tool{
		Name:        "fill_form",
		Description: "Fills multiple form elements at once.",
		InputSchema: schema(`{"type":"object","properties":{
			"elements":{"type":"array","description":"Elements from the snapshot to fill out.","items":{"type":"object","properties":{
				"uid":{"type":"string"},"value":{"type":"string"}
			},"required":["uid","value"]}},
			` + includeSnapshotProp + `
		},"required":["elements"]}`),
	}, wrap(m.fillForm))

	s.RegisterTool(mcpserver.Tool{
		Name:        "type_text",
		Description: "Types text into the currently focused element, optionally pressing a key afterwards.",
		InputSchema: schema(`{"type":"object","properties":{
			"text":{"type":"string","description":"The text to type."},
			"submitKey":{"type":"string","description":"Optional key to press after typing, e.g. \"Enter\", \"Tab\"."}
		},"required":["text"]}`),
	}, wrap(m.typeText))

	s.RegisterTool(mcpserver.Tool{
		Name:        "press_key",
		Description: "Presses a key or combination, e.g. \"Enter\", \"Control+A\", \"Control+Shift+R\". Modifiers: Control, Shift, Alt, Meta.",
		InputSchema: schema(`{"type":"object","properties":{
			"key":{"type":"string","description":"A key or a combination."},
			` + includeSnapshotProp + `
		},"required":["key"]}`),
	}, wrap(m.pressKey))

	s.RegisterTool(mcpserver.Tool{
		Name:        "upload_file",
		Description: "Sets a local file on a file input element.",
		InputSchema: schema(`{"type":"object","properties":{
			"uid":{"type":"string","description":"The uid of the file input element from the page content snapshot."},
			"filePath":{"type":"string","description":"The local path of the file to upload."},
			` + includeSnapshotProp + `
		},"required":["uid","filePath"]}`),
	}, wrap(m.uploadFile))

	s.RegisterTool(mcpserver.Tool{
		Name:        "handle_dialog",
		Description: "Accepts or dismisses the currently open JavaScript dialog (alert/confirm/prompt).",
		InputSchema: schema(`{"type":"object","properties":{
			"action":{"type":"string","enum":["accept","dismiss"],"description":"Whether to accept or dismiss the dialog."},
			"promptText":{"type":"string","description":"Optional prompt text to enter into the dialog."}
		},"required":["action"]}`),
	}, wrap(m.handleDialog))
}

func registerObservabilityTools(s *mcpserver.Server, m *Manager) {
	s.RegisterTool(mcpserver.Tool{
		Name:        "list_console_messages",
		Description: "Lists console messages (log/warn/error and uncaught exceptions) recorded on the selected page. Returns lastMsgId; pass it back as sinceMsgId to see only what appeared since.",
		InputSchema: schema(`{"type":"object","properties":{
			"pageSize":{"type":"integer","description":"Maximum number of messages to return. When omitted, returns all."},
			"pageIdx":{"type":"integer","description":"Page number to return (0-based)."},
			"types":{"type":"array","items":{"type":"string"},"description":"Filter by message types, e.g. [\"error\"]."},
			"sinceMsgId":{"type":"integer","description":"Only return messages with a msgid greater than this (use the lastMsgId from a previous call)."}
		}}`),
	}, wrap(m.listConsoleMessages))

	s.RegisterTool(mcpserver.Tool{
		Name:        "get_console_message",
		Description: "Gets a console message with full details by its msgid from list_console_messages.",
		InputSchema: schema(`{"type":"object","properties":{
			"msgid":{"type":"integer","description":"The msgid of a console message."}
		},"required":["msgid"]}`),
	}, wrap(m.getConsoleMessage))

	s.RegisterTool(mcpserver.Tool{
		Name:        "list_network_requests",
		Description: "Lists network requests recorded on the selected page. Returns lastReqId; pass it back as sinceReqId to see only what happened since.",
		InputSchema: schema(`{"type":"object","properties":{
			"pageSize":{"type":"integer","description":"Maximum number of requests to return. When omitted, returns all."},
			"pageIdx":{"type":"integer","description":"Page number to return (0-based)."},
			"resourceTypes":{"type":"array","items":{"type":"string"},"description":"Filter by resource types, e.g. [\"XHR\",\"Fetch\"]."},
			"sinceReqId":{"type":"integer","description":"Only return requests with a reqid greater than this (use the lastReqId from a previous call)."},
			"failedOnly":{"type":"boolean","description":"Only return requests that failed or returned status >= 400."}
		}}`),
	}, wrap(m.listNetworkRequests))

	s.RegisterTool(mcpserver.Tool{
		Name:        "get_network_request",
		Description: "Gets a network request by its reqid from list_network_requests, including headers and (truncated) response body.",
		InputSchema: schema(`{"type":"object","properties":{
			"reqid":{"type":"integer","description":"The reqid of the network request."}
		},"required":["reqid"]}`),
	}, wrap(m.getNetworkRequest))

	s.RegisterTool(mcpserver.Tool{
		Name:        "resize_page",
		Description: "Resizes the selected page's viewport.",
		InputSchema: schema(`{"type":"object","properties":{
			"width":{"type":"number","description":"Page width."},
			"height":{"type":"number","description":"Page height."}
		},"required":["width","height"]}`),
	}, wrap(m.resizePage))

	s.RegisterTool(mcpserver.Tool{
		Name:        "emulate",
		Description: "Emulates device/environment conditions on the selected page: color scheme, CPU throttling, network throttling, geolocation, user agent, viewport, extra HTTP headers. Every call sets the whole emulation state, so an omitted parameter is reset — call it with no arguments to clear everything. The one exception is extraHttpHeaders, which is only touched when provided. The response reports the effective value of every dimension. Note: after clearing \"Offline\", Chrome reloads the error page on its own, so a URL that failed while offline can end up looking loaded.",
		InputSchema: schema(`{"type":"object","properties":{
			"colorScheme":{"type":"string","enum":["dark","light","auto"],"description":"Emulate dark or light mode; \"auto\" or omitted resets."},
			"cpuThrottlingRate":{"type":"number","description":"CPU slowdown factor. Omit or 1 to disable."},
			"extraHttpHeaders":{"type":"string","description":"Extra HTTP headers as a JSON object string, e.g. {\"X-Custom\":\"value\"}. Empty string clears them; omitting leaves them unchanged."},
			"geolocation":{"type":"string","description":"\"<latitude>,<longitude>\" to emulate. Omit to clear."},
			"networkConditions":{"type":"string","enum":["Offline","Slow 3G","Fast 3G","Slow 4G","Fast 4G"],"description":"Throttle network. Omit to disable."},
			"userAgent":{"type":"string","description":"User agent override. Omit or pass an empty string to clear it."},
			"viewport":{"type":"string","description":"\"<width>x<height>[x<dpr>][,mobile][,touch][,landscape]\". Omit to clear."}
		}}`),
	}, wrap(m.emulate))

	s.RegisterTool(mcpserver.Tool{
		Name:        "screencast_start",
		Description: "Starts recording the selected page as an animated GIF (frames are captured until screencast_stop). A viewport change mid-recording is fine: frames are refitted rather than dropped.",
		InputSchema: schema(`{"type":"object","properties":{
			"filePath":{"type":"string","description":"Output .gif path. Defaults to the workspace."},
			"maxWidth":{"type":"integer","description":"Max frame width in px. Default 800."},
			"everyNthFrame":{"type":"integer","description":"Capture every Nth frame. Default 2."},
			"quality":{"type":"integer","description":"JPEG capture quality 0-100. Default 70."},
			"maxFrames":{"type":"integer","description":"Stop collecting after this many frames. Default 600."},
			"maxDurationMs":{"type":"integer","description":"Stop collecting after this much wall-clock time. Unlimited by default."}
		}}`),
	}, wrap(m.screencastStart))

	s.RegisterTool(mcpserver.Tool{
		Name:        "screencast_stop",
		Description: "Stops the recording and writes the animated GIF; returns its path, size, frame count, duration, and whether the recording was truncated by a limit.",
		InputSchema: schema(`{"type":"object","properties":{}}`),
	}, wrap(m.screencastStop))
}
