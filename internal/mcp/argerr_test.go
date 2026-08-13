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

// Every error an agent can provoke carries the corrective call.
//
// The rule (AGENTS.md) is that every error tells a drifted agent what to do
// instead. Tool errors carry it in the result payload, but arguments that do
// not decode never reach a handler, so these three were the last errors on this
// surface with no hint at all. Found by sending register the pre-0.0.3 nested
// `agent` object: the answer was `agent must be a string, got object` and
// nothing about where the current shape is written down.
func TestEveryArgumentErrorCarriesAHint(t *testing.T) {
	srv, _ := newServer(t)

	for _, tc := range []struct {
		name   string
		params any
		want   string // a word the hint must contain
	}{
		{
			name:   "a parameter of the wrong type",
			params: map[string]any{"name": "register", "arguments": map[string]any{"agent": map[string]any{"cwd": "/tmp"}}},
			want:   "register",
		},
		{
			name:   "a required parameter missing",
			params: map[string]any{"name": "declare", "arguments": map[string]any{}},
			want:   "declare",
		},
		{
			name:   "params that are not an object at all",
			params: map[string]any{"name": "register", "arguments": "not-an-object"},
			want:   "tools/list",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := rpc(t, srv, "2026-07-28", "tools/call", tc.params)
			rpcErr, ok := out["error"].(map[string]any)
			if !ok {
				t.Fatalf("setup: expected a protocol error, got %v", out)
			}
			data, ok := rpcErr["data"].(map[string]any)
			if !ok {
				t.Fatalf("no data on the error, so no hint: %v", rpcErr)
			}
			h, _ := data["hint"].(string)
			if h == "" {
				t.Fatal("the error carries no hint: a drifted agent is told what is wrong and nothing about what to do")
			}
			if !strings.Contains(h, "tools/list") {
				t.Errorf("the hint must name the call that answers the question, got %q", h)
			}
			if !strings.Contains(h, tc.want) {
				t.Errorf("hint %q does not mention %q", h, tc.want)
			}
		})
	}
}
