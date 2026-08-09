package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// encoding/json's own message names our internal struct and the Go type system:
// "json: cannot unmarshal string into Go struct field toolArgs.pid of type int".
// An agent cannot act on that, and it leaks an implementation detail. A live
// glm-4.6 run hit it by sending `"pid": "$$"` and only recovered by shelling out
// to echo the value.
func TestArgErrIsActionableAndLeaksNoInternals(t *testing.T) {
	var a toolArgs
	err := json.Unmarshal([]byte(`{"pid":"$$"}`), &a)
	if err == nil {
		t.Fatal("expected a type error")
	}
	got := argErr(err)

	if !strings.Contains(got, "pid") {
		t.Errorf("must name the offending field, got %q", got)
	}
	if !strings.Contains(got, "number") {
		t.Errorf("must say what was wanted in plain words, got %q", got)
	}
	if strings.Contains(got, "toolArgs") || strings.Contains(got, "Go struct") {
		t.Errorf("must not leak internals, got %q", got)
	}
}

// Anything that is not a type mismatch passes through untouched: inventing a
// friendlier wording for an error we have not classified would hide it.
func TestArgErrPassesThroughUnknownErrors(t *testing.T) {
	var a toolArgs
	err := json.Unmarshal([]byte(`{not json`), &a)
	if err == nil {
		t.Fatal("expected a syntax error")
	}
	if argErr(err) != err.Error() {
		t.Errorf("unclassified errors must pass through, got %q", argErr(err))
	}
}
