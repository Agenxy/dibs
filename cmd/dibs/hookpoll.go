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
// THAT NAMED THE RIGHT EVENT AND NOW POINTS AT THE WRONG END OF THE TURN.
// Re-measured 2026-09-25 at 20f7075: `AfterAgent` still cannot carry context,
// exactly as above, but `BeforeAgent` now accepts `additionalContext` and is
// dispatched for real (`client.ts:931`), so a per-turn delivery point exists
// at the START of a turn, where nothing has to be rejected to use it. The same
// survey found Gemini's hook input now carries a populated `session_id` and a
// `transcript_path`, so an agent there need not be found by its directory.
//
// Neither is wired up: this still forwards SessionStart and nothing else. Said
// out loud because a stale "the harness cannot" is how the SessionStart
// mcp_tool hook sat broken for six weeks. What is missing is a measurement
// against a live Gemini session, not a reason.
//
// # When it fails
//
// An event this harness cannot deliver at is not a failure: `{}`, exit 0, and
// the reason on stderr for the logs. A daemon that did not answer, or input
// that is not the hook's JSON, IS one, and is reported as one: nothing on
// stdout, the reason on stderr, exit 1. The first version printed `{}` and
// exited 0 for those too, so that a person never saw a warning at launch, and
// so a daemon that had been down for a week was indistinguishable from a
// board with nothing to say. Gemini shows a non-zero hook's stderr as a
// warning and carries on, which is the right cost: the person learns the wake
// path is dead on the launch where it died, not from a peer whose question
// expired. The Codex review of #101 found the silent version.
//
// The input is bounded before it is decoded: a hook's stdin is whatever the
// harness wrote, and the plugin's timeout bounds time, not memory. And it is
// decoded WHOLE: a streaming decoder stops at the first value, so a valid
// object followed by garbage was "JSON", which the Codex review of this
// change caught. What the harness wrote is one object and nothing after it.
func hookPoll(args []string) error {
	_ = args // the dispatch table in main.go requires this signature
	out, err := hookPollOutput(os.Stdin, callHookTool)
	if err != nil {
		return fmt.Errorf("hook-poll: %w", err)
	}
	enc, err := json.Marshal(out)
	if err != nil {
		return fmt.Errorf("hook-poll: %w", err)
	}
	_, err = os.Stdout.Write(append(enc, '\n'))
	return err
}

// hookInputLimit bounds what is read from the harness before decoding. A
// hook's input is a handful of short fields; a megabyte is a thousand times
// that, and past it the input is not a hook's.
const hookInputLimit = 1 << 20

// hookPollOutput is the decision, split from the process so it can be tested
// against a fake daemon: what the harness said in, what goes out, and whether
// that is a failure.
func hookPollOutput(r io.Reader, call func(tool string, args map[string]any, out any) error) (map[string]any, error) {
	var in struct {
		SessionID string `json:"session_id"`
		CWD       string `json:"cwd"`
		Event     string `json:"hook_event_name"`
	}
	raw, err := io.ReadAll(io.LimitReader(r, hookInputLimit+1))
	if err != nil {
		return nil, fmt.Errorf("reading hook input: %w", err)
	}
	if len(raw) > hookInputLimit {
		return nil, fmt.Errorf("hook input is over %d bytes, which no hook's is", hookInputLimit)
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("hook input is not one JSON object: %w", err)
	}
	if in.Event != "SessionStart" {
		// See the header: this harness cannot carry a digest at any other
		// boundary without rejecting the agent's own answer.
		fmt.Fprintf(os.Stderr, "dibs hook-poll: %s is not a boundary this harness can deliver mail at; "+
			"only SessionStart is forwarded\n", in.Event)
		return map[string]any{}, nil
	}
	var res map[string]any
	if err := call("hook_poll", map[string]any{
		"session_id": in.SessionID, "event": in.Event, "cwd": in.CWD, "strict_output": "true",
	}, &res); err != nil {
		return nil, fmt.Errorf("the daemon did not answer: %w", err)
	}
	// `agent` is what the daemon says when there is nothing to inject; a
	// hook schema has no field for it, and strict output already dropped
	// everything else a schema cannot carry.
	delete(res, "agent")
	if res == nil {
		return map[string]any{}, nil
	}
	return res, nil
}
