package toolerr_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nlink-jp/chrome-pilot-mcp/internal/toolerr"
)

func TestErrorString(t *testing.T) {
	e := toolerr.New(toolerr.CodePageNotFound, "no page at index 3")
	if got := e.Error(); got != "page_not_found: no page at index 3" {
		t.Errorf("got %q", got)
	}
}

func TestErrorIsByCode(t *testing.T) {
	sentinel := toolerr.New(toolerr.CodePageNotFound, "")
	actual := toolerr.Newf(toolerr.CodePageNotFound, "no page at index %d", 3)
	if !errors.Is(actual, sentinel) {
		t.Errorf("errors.Is should match by Code")
	}
	other := toolerr.New(toolerr.CodeElementNotFound, "")
	if errors.Is(actual, other) {
		t.Errorf("errors.Is should not match a different Code")
	}
}

func TestErrorWrappedIs(t *testing.T) {
	inner := toolerr.New(toolerr.CodeCDPError, "target crashed")
	wrapped := fmt.Errorf("navigate: %w", inner)
	if !errors.Is(wrapped, toolerr.New(toolerr.CodeCDPError, "")) {
		t.Errorf("errors.Is should walk wrapper chain")
	}
}

func TestErrorJSONMarshal(t *testing.T) {
	e := toolerr.New(toolerr.CodeCDPError, "boom").WithDetails(map[string]any{
		"method": "Page.navigate",
		"code":   -32000,
	})
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"code":"cdp_error"`, `"message":"boom"`, `"method":"Page.navigate"`} {
		if !strings.Contains(s, want) {
			t.Errorf("marshaled error missing %q: %s", want, s)
		}
	}
}

func TestWithDetailsDoesNotMutate(t *testing.T) {
	e := toolerr.New(toolerr.CodePageNotFound, "x")
	_ = e.WithDetails(map[string]any{"k": "v"})
	if e.Details != nil {
		t.Errorf("WithDetails should not mutate receiver")
	}
}
