package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// hookPoll is `dibs hook-poll`: the wake path for a harness whose hooks are
// subprocesses reading JSON on stdin and writing JSON on stdout. Gemini CLI is
// the first; its hooks are `command` type only (issue #24).
//
// # What it does
//
// It reads the harness's hook input (session_id, cwd, hook_event_name), asks
// the daemon's hook_poll the same question the Claude Code and Codex
// `mcp_tool` hooks ask, and prints what the daemon answered, in the strict
// shape a hook schema accepts. Nothing else goes to stdout: Gemini treats any
// other byte there as a parse failure and shows the lot as a system message.
//
// # What it refuses to do
//
// Only SessionStart is forwarded, and that is a limit of the harness, not a
// choice about it. Gemini's SessionStart accepts `additionalContext`, so a
// digest lands as the first turn's context. Its AfterAgent (the end of a
// turn, where Claude Code and Codex deliver) offers no way to add context: the
// only outputs are `decision: deny`, which REJECTS the model's answer and sends
// the hook's text as a new prompt, and `continue: false`, which stops the
// session. Delivering mail by discarding the agent's reply is the board
// steering the agent (AGENTS.md rule 5), so it is not done, and a hook that
// told the daemon "Stop" without being able to carry the digest would spend
// the one-shot wake on nothing. Past session start, Gemini is pull-only:
// check_in each activation, await_events before blocking.
//
// # Never a failure
//
// A hook that exits non-zero or prints garbage costs the person a warning on
// every session start. Whatever goes wrong here, the output is `{}` and the
// exit status is 0; the reason goes to stderr, which Gemini keeps for logs.
// a hook that could fail would cost the person a warning on every launch.
//
//nolint:unparam // the dispatch table in main.go requires this signature, and
func hookPoll(args []string) error {
	_ = args
	out := hookPollOutput(os.Stdin, callHookTool)
	enc, err := json.Marshal(out)
	if err != nil {
		enc = []byte("{}")
	}
	fmt.Println(string(enc))
	return nil
}

// hookPollOutput is the decision, split from the process so it can be tested
// against a fake daemon: what the harness said in, what goes out.
func hookPollOutput(r io.Reader, call func(tool string, args map[string]any, out any) error) map[string]any {
	var in struct {
		SessionID string `json:"session_id"`
		CWD       string `json:"cwd"`
		Event     string `json:"hook_event_name"`
		StopHook  any    `json:"stop_hook_active"`
	}
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		fmt.Fprintln(os.Stderr, "dibs hook-poll: hook input is not JSON:", err)
		return map[string]any{}
	}
	if in.Event != "SessionStart" {
		// See the header: this harness cannot carry a digest at any other
		// boundary without rejecting the agent's own answer.
		fmt.Fprintf(os.Stderr, "dibs hook-poll: %s is not a boundary this harness can deliver mail at; "+
			"only SessionStart is forwarded\n", in.Event)
		return map[string]any{}
	}
	var res map[string]any
	if err := call("hook_poll", map[string]any{
		"session_id": in.SessionID, "event": in.Event, "cwd": in.CWD,
		"stop_hook_active": in.StopHook, "strict_output": "true",
	}, &res); err != nil {
		fmt.Fprintln(os.Stderr, "dibs hook-poll: the daemon did not answer:", err)
		return map[string]any{}
	}
	// `agent` is what the daemon says when there is nothing to inject; a
	// hook schema has no field for it, and strict output already dropped
	// everything else a schema cannot carry.
	delete(res, "agent")
	if res == nil {
		return map[string]any{}
	}
	return res
}
