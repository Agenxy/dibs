package main

import (
	"errors"
	"strings"
	"testing"
)

// Gemini's hooks are subprocesses, and its SessionStart is the one boundary
// where a hook can add context without rejecting the agent's own answer. So
// that event is forwarded to the daemon's hook_poll in strict shape, every
// other event is answered with an empty object, and nothing is ever an error:
// a failing hook costs the person a warning on every launch. Issue #24.
func TestHookPollForwardsSessionStartAndStaysSilentElsewhere(t *testing.T) {
	var asked []map[string]any
	daemon := func(tool string, args map[string]any, out any) error {
		asked = append(asked, args)
		*(out.(*map[string]any)) = map[string]any{
			"agent": "quiet",
			"hookSpecificOutput": map[string]any{
				"hookEventName": "SessionStart", "additionalContext": "Dibs: 1 unread message(s)",
			},
			"systemMessage": "Dibs · 1 unread for quiet",
		}
		return nil
	}

	out := hookPollOutput(strings.NewReader(`{"session_id":"g-1","cwd":"/work","hook_event_name":"SessionStart"}`), daemon)
	if len(asked) != 1 || asked[0]["event"] != "SessionStart" || asked[0]["session_id"] != "g-1" ||
		asked[0]["strict_output"] != "true" {
		t.Fatalf("daemon asked %v, want one strict hook_poll for the session", asked)
	}
	hso, _ := out["hookSpecificOutput"].(map[string]any)
	if hso["additionalContext"] != "Dibs: 1 unread message(s)" {
		t.Errorf("output = %v, want the daemon's digest under hookSpecificOutput.additionalContext, "+
			"which is the field Gemini injects", out)
	}
	if _, leaked := out["agent"]; leaked {
		t.Error("output carries `agent`, which no hook schema has a field for")
	}

	// The end of a turn: Gemini can only reject the answer or stop the
	// session there, so nothing is asked and nothing is said.
	asked = nil
	out = hookPollOutput(strings.NewReader(`{"session_id":"g-1","cwd":"/work","hook_event_name":"AfterAgent"}`), daemon)
	if len(asked) != 0 || len(out) != 0 {
		t.Errorf("AfterAgent asked %v and printed %v, want nothing: a wake spent on a "+
			"boundary that cannot carry it is a wake lost", asked, out)
	}

	// A daemon that is down, and input that is not JSON: `{}`, never a failure.
	down := func(string, map[string]any, any) error { return errors.New("connection refused") }
	if out := hookPollOutput(strings.NewReader(`{"hook_event_name":"SessionStart"}`), down); len(out) != 0 {
		t.Errorf("daemon down printed %v, want {}", out)
	}
	if out := hookPollOutput(strings.NewReader(`not json`), daemon); len(out) != 0 {
		t.Errorf("bad input printed %v, want {}", out)
	}
}
