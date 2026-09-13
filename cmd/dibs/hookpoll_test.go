package main

import (
	"errors"
	"strings"
	"testing"
)

// Gemini's hooks are subprocesses, and its SessionStart is the one boundary
// where a hook can add context without rejecting the agent's own answer. So
// that event is forwarded to the daemon's hook_poll in strict shape, and
// every other event is answered with an empty object, because a boundary the
// harness cannot deliver at is not a failure. Issue #24.
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

	out, err := hookPollOutput(strings.NewReader(`{"session_id":"g-1","cwd":"/work","hook_event_name":"SessionStart"}`), daemon)
	if err != nil {
		t.Fatal(err)
	}
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
	if _, sent := asked[0]["stop_hook_active"]; sent {
		t.Error("stop_hook_active was forwarded: it is a Stop-hook field, and Stop is never forwarded")
	}

	// The end of a turn: Gemini can only reject the answer or stop the
	// session there, so nothing is asked and nothing is said.
	asked = nil
	out, err = hookPollOutput(strings.NewReader(`{"session_id":"g-1","cwd":"/work","hook_event_name":"AfterAgent"}`), daemon)
	if err != nil || len(asked) != 0 || len(out) != 0 {
		t.Errorf("AfterAgent asked %v, printed %v and failed with %v, want nothing and no failure: "+
			"a wake spent on a boundary that cannot carry it is a wake lost, and a boundary the "+
			"harness lacks is not the daemon's fault", asked, out, err)
	}
}

// A daemon that did not answer, and input that is not the hook's JSON, are
// failures and are reported as failures: the first version printed `{}` and
// exited 0 for both, so a dead wake path looked exactly like a quiet board.
// Found by the Codex review of #101.
func TestHookPollReportsADeadDaemonAndBadInputAsFailures(t *testing.T) {
	quiet := func(string, map[string]any, any) error { return nil }
	down := func(string, map[string]any, any) error { return errors.New("connection refused") }
	out, err := hookPollOutput(strings.NewReader(`{"hook_event_name":"SessionStart"}`), down)
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("daemon down: printed %v, err %v; want the failure, with the reason", out, err)
	}
	if out, err := hookPollOutput(strings.NewReader(`not json`), quiet); err == nil {
		t.Errorf("bad input: printed %v with no error", out)
	}
	// Bounded before it is decoded: a value past the limit is refused, not
	// buffered, however well-formed.
	huge := `{"hook_event_name":"SessionStart","pad":"` + strings.Repeat("x", hookInputLimit) + `"}`
	if out, err := hookPollOutput(strings.NewReader(huge), quiet); err == nil {
		t.Errorf("an input past %d bytes was decoded: %v", hookInputLimit, out)
	}
	// And decoded whole: a valid object with anything after it is not the
	// hook's input. A streaming decoder took the first value and ignored the
	// rest, so these succeeded; found by the Codex review of this change.
	for _, tail := range []string{`{"hook_event_name":"SessionStart"}garbage`, `{"hook_event_name":"SessionStart"}{"hook_event_name":"SessionStart"}`} {
		if out, err := hookPollOutput(strings.NewReader(tail), quiet); err == nil {
			t.Errorf("%s: printed %v with no error; the daemon was asked on input that is not one object", tail, out)
		}
	}
	// And a valid empty poll still succeeds.
	if out, err := hookPollOutput(strings.NewReader(`{"hook_event_name":"SessionStart"}`), quiet); err != nil || len(out) != 0 {
		t.Errorf("a quiet board: printed %v, err %v; want {} and no failure", out, err)
	}
}
