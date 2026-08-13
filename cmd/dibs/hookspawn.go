package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/agenxy/dibs/internal/liveness"
)

// hookSpawn stamps a spawned subagent with the agent that spawned it.
//
// # Why this exists
//
// Everything else in supervision has to work out whose subagent a process is
// AFTER the fact, and the honest mechanisms are all inference. Process ancestry
// is destroyed the moment a child is detached, which is exactly when a harness
// detaches it. Reading a harness's session directory off the child's PATH works
// today and is nobody's documented interface. Both are fallbacks worth having
// and neither is a foundation.
//
// A PreToolUse hook can rewrite the tool's input before it runs
// (`hookSpecificOutput.updatedInput`, supported by both Codex and Claude Code).
// So the parent stamps the child at the only moment when the relationship is a
// FACT rather than a reconstruction (the instant it spawns it) by prefixing
// one environment assignment onto the command. The OS does the rest: an
// environment is inherited at fork and survives reparenting, daemonisation and
// process-group changes, so every descendant carries it, however deep and
// however detached.
//
// That turns attribution from a ladder of guesses into a fact with fallbacks.
//
// # What it will not do
//
// It runs before EVERY shell command an agent issues, so its first duty is to
// be harmless. It emits a rewrite only when all of these hold, and otherwise
// prints nothing at all and lets the command through untouched:
//
//   - the command actually spawns an agent (the same executable-basename test
//     the process sweep uses: a command that merely MENTIONS codex is not one)
//   - the calling session maps to an agent on the board
//   - the command is not already stamped
//   - the command has no shell syntax that a prefix would change the meaning of
//
// A hook that mangles a command is infinitely worse than one that does nothing:
// the agent's work fails in a way it cannot diagnose, on a machine where the
// user did not know a hook was involved.
// the nil is the point: see the comment below.
//
//nolint:unparam // the dispatch table in main.go requires this signature, and
func hookSpawn(args []string) error {
	_ = args
	// Every failure path prints nothing and succeeds. A hook runs in front of an
	// agent's shell command: if it cannot decide, the command must proceed
	// untouched, and a non-zero exit or a stray line on stdout is a way to break
	// work that the agent cannot diagnose.
	if out := stampFor(os.Stdin, agentForSession); out != "" {
		fmt.Println(out)
	}
	return nil
}

// stampFor decides what, if anything, to emit for one hook invocation.
//
// Separated from hookSpawn so the decision can be tested without a process, a
// daemon or a pipe. `resolve` is the only thing it needs from the outside
// world, and a test supplies it directly. Returns "" for "say nothing", which
// is the answer in every case but one.
func stampFor(r io.Reader, resolve func(sessionID, cwd string) string) string {
	var in struct {
		SessionID string          `json:"session_id"`
		CWD       string          `json:"cwd"`
		ToolName  string          `json:"tool_name"`
		ToolInput json.RawMessage `json:"tool_input"`
	}
	if json.NewDecoder(r).Decode(&in) != nil {
		return ""
	}
	var input map[string]any
	if json.Unmarshal(in.ToolInput, &input) != nil {
		return ""
	}
	cmd, _ := input["command"].(string)
	if cmd == "" {
		return "" // not a shell tool
	}
	if strings.Contains(cmd, "DIBS_PARENT=") {
		return "" // already stamped, by an outer hook or by the agent itself
	}
	if !spawnsAgent(cmd) || !safeToPrefix(cmd) {
		return ""
	}
	agent := resolve(in.SessionID, in.CWD)
	if agent == "" {
		return ""
	}
	input["command"] = "DIBS_PARENT=" + agent + " " + cmd
	enc, err := json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName": "PreToolUse",
			"updatedInput":  input,
		},
	})
	if err != nil {
		return ""
	}
	return string(enc)
}

// spawnsAgent reports whether a shell command launches an agent.
//
// Reuses the process sweep's judgement so the two cannot disagree about what an
// agent is: a command stamped here but not recognised there would be attributed
// and never watched, and the reverse would be watched and never attributed.
//
// Only the FIRST command in the line is considered. `cd /x && codex exec …`
// spawns an agent, but the prefix would land on `cd`, where the assignment
// applies to `cd` alone and never reaches the agent: a stamp that silently
// does nothing is worse than no stamp, because the fallback rungs would have
// caught it.
func spawnsAgent(cmd string) bool {
	first := cmd
	for _, sep := range []string{"&&", "||", ";", "|"} {
		if i := strings.Index(first, sep); i >= 0 {
			first = first[:i]
		}
	}
	return liveness.HarnessOf(strings.TrimSpace(first)) != ""
}

// safeToPrefix rejects commands where a leading assignment would change the
// meaning of what the agent asked to run.
//
// A VAR=x prefix is only equivalent to the original for a plain command. Put it
// in front of a subshell, a redirect that opens the line, or a shell builtin
// with its own parsing, and the result is either a syntax error or a different
// program. The refusal costs a fallback rung; a mangled command costs the
// agent's work.
func safeToPrefix(cmd string) bool {
	t := strings.TrimSpace(cmd)
	if t == "" {
		return false
	}
	// A line that starts with anything but a bare word is not ours to rewrite.
	//
	// Defence in depth rather than load-bearing today: spawnsAgent already
	// rejects every one of these, because a command opening with `(`, `{`, `<`,
	// `>`, `$`, a backtick or a quote has a first TOKEN that is not a bare
	// harness name. Measured, not assumed: removing this check changes no
	// reachable answer. It is kept because that is a property of how
	// spawnsAgent tokenises, not a decision anybody made, and loosening
	// spawnsAgent later (matching a harness anywhere in the line, say) would
	// make these the only thing standing between a rewrite and a mangled
	// command.
	switch t[0] {
	case '(', '{', '<', '>', '#', '!', '$', '`', '"', '\'':
		return false
	}
	// An assignment already leads the line; adding another changes evaluation
	// order in ways that depend on the shell. Also currently unreachable for
	// the same reason as above, and kept for the same reason.
	if head, _, ok := strings.Cut(t, " "); ok && strings.Contains(head, "=") {
		return false
	}
	// Newlines mean the "command" is a script, and a prefix binds to its first
	// line only, which is the same silent-no-op as the `cd &&` case. THIS one
	// is load-bearing: `codex exec 'a'\nrm -rf /` passes spawnsAgent, so
	// without this check it would be rewritten.
	return !strings.ContainsAny(t, "\n\r")
}

// agentForSession asks the daemon which agent owns a harness session.
//
// Via hook_poll, which is the existing token-less lifecycle-hook path: a hook
// has no agent token, and inventing a second unauthenticated endpoint for this
// would widen the surface that path was carefully narrowed to.
func agentForSession(sessionID, cwd string) string {
	if sessionID == "" {
		return ""
	}
	var out struct {
		Agent string `json:"agent"`
	}
	if err := callHookTool("hook_poll", map[string]any{
		"session_id": sessionID, "event": "PreToolUse", "cwd": cwd,
	}, &out); err != nil {
		return ""
	}
	return out.Agent
}
