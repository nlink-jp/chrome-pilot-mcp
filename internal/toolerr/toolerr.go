// Package toolerr defines a structured tool-error type that MCP tools return
// to clients. Each error carries a stable code (slug) that LLM clients can
// branch on, plus a human-readable message and optional details.
//
// The error type satisfies the standard error interface, and its Is method
// compares by Code so errors.Is works with sentinel values regardless of the
// inner Message.
package toolerr

import "fmt"

// Error is a structured tool error.
type Error struct {
	// Code is a stable slug for client-side branching (e.g. "page_not_found").
	Code string `json:"code"`
	// Message is a human-readable summary.
	Message string `json:"message"`
	// Details carries machine-readable context (e.g. requested page index, CDP method).
	Details map[string]any `json:"details,omitempty"`
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// Is reports whether target is a *Error with the same Code. This lets sentinel
// values work under errors.Is regardless of the inner Message and Details.
func (e *Error) Is(target error) bool {
	te, ok := target.(*Error)
	if !ok {
		return false
	}
	return te.Code == e.Code
}

// WithDetails returns a copy of e with the given details attached.
func (e *Error) WithDetails(d map[string]any) *Error {
	cp := *e
	cp.Details = d
	return &cp
}

// New creates an Error.
func New(code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// Newf creates an Error with a printf-formatted message.
func Newf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Stable error codes used across the tool implementations. Adding a new code
// is a no-op for older clients (they fall back to inspecting Message), but
// renaming an existing code is a breaking change.
const (
	CodeInvalidArguments    = "invalid_arguments"
	CodeMissingArgument     = "missing_argument"
	CodeBrowserLaunchFailed = "browser_launch_failed"
	CodeAttachFailed        = "attach_failed"
	CodePageNotFound        = "page_not_found"
	CodeElementNotFound     = "element_not_found"
	CodeDialogNotOpen       = "dialog_not_open"
	CodeScreencastNotActive = "screencast_not_active"
	CodeHostNotAllowed      = "host_not_allowed"
	CodeCDPError            = "cdp_error"
	CodeScriptFailed        = "script_failed"
	CodeTimeout             = "timeout"
	CodeWorkspaceFailed     = "workspace_failed"
)
