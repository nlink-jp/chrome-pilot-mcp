package tools

// Instructions is the initialize-time hint a client hands to its model before
// any tool list (the MCP `instructions` field; cmd sets it on the server).
//
// It is prose the model acts on, so every name in it is checked against what
// RegisterAll actually registers (workdir_contract_test.go). There is no
// usage tool on this server; list_pages and take_snapshot are how a model
// finds its footing, so those are what this points at.
const Instructions = "chrome-pilot-mcp drives a Chrome browser on this machine over the Chrome DevTools Protocol: " +
	"it opens and navigates pages, reads them, clicks and types into them, and records their console messages " +
	"and network requests. " +
	"Call list_pages first to see the open tabs and which one is selected, since most tools work on the selected " +
	"page, then take_snapshot for the uids that click, fill, hover and the other element tools take; each new " +
	"snapshot replaces the previous uids. " +
	"Three tools take work_dir, take_screenshot, screencast_start and upload_file, and in all three it is required " +
	"with no default: the absolute path of an existing directory you can read back (your session or working directory). " +
	"The screenshot is written under <work_dir>/screenshots/, and the GIF that screencast_stop produces under " +
	"<work_dir>/screencasts/, or at the filePath given to screencast_start, which must lie under work_dir too; both " +
	"results return the file's path. upload_file hands a page only a file under work_dir, so copy a file there first. " +
	"When an action opens an alert, confirm or prompt, its result says so and the page stays blocked until you " +
	"call handle_dialog."
