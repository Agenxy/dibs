package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// hookSession is `dibs hook-session`: the SessionStart half of the Claude Code
// plugin, as a subprocess rather than an MCP tool call.
//
// WHY IT IS NOT AN mcp_tool HOOK, WHICH IS WHAT IT USED TO BE. Claude Code
// resolves an `mcp_tool` hook against the session's connected MCP clients, and
// at SessionStart there are none yet: the hook is skipped with "mcp_tool hooks
// are not available for the 'SessionStart' hook event (no MCP client context)"
// and a non-blocking error in the transcript. Measured 2026-09-24 across this
// operator's own history: 307 of those, in 22 projects, from 2026-08-14 to
// that morning, every one of them a Dibs hook. Neither SessionStart hook had
// ever run, in any session, on any version.
//
// That is the expensive half of it. The cheap half is that a hook which fails
// at every single session start is a warning nobody can read: it had been
// arriving for six weeks beside the sessions it was failing to serve.
//
// A `command` hook has no such dependency. It reads the harness's hook input
// on stdin, which is where the session id and the transcript path are, and it
// reaches the daemon over the same local socket every other CLI call uses.
//
// It prints `{}` and exits 0 on success, because this hook has nothing to say
// to the model: it is a report TO the board, not a delivery FROM it. Mail at
// session start is `dibs hook-poll`, which is the other entry in this event.
func hookSession(args []string) error {
	_ = args // the dispatch table in main.go requires this signature
	if err := hookSessionReport(os.Stdin, callHookTool); err != nil {
		return fmt.Errorf("hook-session: %w", err)
	}
	_, err := os.Stdout.Write([]byte("{}\n"))
	return err
}

// hookSessionReport is the decision, split from the process so it can be
// tested against a fake daemon.
//
// A session with no id is not an error and not a call: the harness gave us
// nothing to bind, and asking the daemon to record "" would put an
// unaddressable row on the board. The same bound-then-decoded reading as
// hook-poll, for the same reason.
func hookSessionReport(r io.Reader, call func(tool string, args map[string]any, out any) error) error {
	var in struct {
		SessionID      string `json:"session_id"`
		CWD            string `json:"cwd"`
		Event          string `json:"hook_event_name"`
		TranscriptPath string `json:"transcript_path"`
	}
	raw, err := io.ReadAll(io.LimitReader(r, hookInputLimit+1))
	if err != nil {
		return fmt.Errorf("reading hook input: %w", err)
	}
	if len(raw) > hookInputLimit {
		return fmt.Errorf("hook input is over %d bytes, which no hook's is", hookInputLimit)
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("hook input is not one JSON object: %w", err)
	}
	if in.SessionID == "" {
		fmt.Fprintln(os.Stderr, "dibs hook-session: the harness sent no session_id, so there is "+
			"nothing to bind; nothing was reported")
		return nil
	}
	args := map[string]any{"session_id": in.SessionID, "event": in.Event, "cwd": in.CWD}
	if in.TranscriptPath != "" {
		args["transcript_path"] = in.TranscriptPath
	}
	var res map[string]any
	if err := call("hook_session", args, &res); err != nil {
		return fmt.Errorf("the daemon did not answer: %w", err)
	}
	return nil
}
