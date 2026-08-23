package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A strict hook response may carry ONLY what a hook-output schema accepts.
//
// Codex validates against Rust structs with deny_unknown_fields at every level:
// continue, stopReason, suppressOutput, systemMessage, and hookSpecificOutput
// {hookEventName, additionalContext}. One key it does not recognise fails the
// whole parse, so the hook is reported FAILED and any additionalContext is
// discarded. Claude Code ignores extras, which is why hook_poll was free to
// answer with `agent` and `queued` and why nobody noticed.
//
// Measured against a running daemon before this was written: hook_poll on
// UserPromptSubmit for an agent with unread mail returned exactly
// {"agent":…,"queued":…}. Both rejected.
func TestAStrictHookResponseCarriesOnlySchemaKeys(t *testing.T) {
	// Transcribed from codex-rs 8e649e3a, hooks/src/schema.rs:
	// HookUniversalOutputWire and SessionStartCommandOutputWire.
	allowed := map[string]bool{
		"continue": true, "stopReason": true, "suppressOutput": true,
		"systemMessage": true, "hookSpecificOutput": true,
	}

	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "asker", Nonce: "n-asker",
		AgentKind: core.KindPersistent, SessionID: "sess-1",
	}); err != nil {
		t.Fatalf("setup: register: %v", err)
	}
	var id string
	for agentID := range st.Agents {
		id = agentID
	}
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, To: id, MsgType: core.MsgQuestion,
		Body: "something somebody is waiting on",
	}); err != nil {
		t.Logf("setup: send returned %v (the mail is not essential; the key set is)", err)
	}

	// Every event, because the branch that produced the offending keys is the
	// one an event cannot carry news on, and UserPromptSubmit is always that.
	for _, event := range []string{"SessionStart", "Stop", "SubagentStop", "UserPromptSubmit"} {
		t.Run(event, func(t *testing.T) {
			res, err := e.HookPoll(ctx, "sess-1", event, "", false, true)
			if err != nil {
				t.Fatalf("hook_poll: %v", err)
			}
			for k := range res {
				if !allowed[k] {
					t.Errorf("strict response carries %q, which Codex's deny_unknown_fields "+
						"refuses: the hook is reported failed and any additionalContext in "+
						"the same object is thrown away. Keys: %v", k, keysOf(res))
				}
			}
		})
	}

	// And the same call WITHOUT strict keeps the diagnosis, because Claude Code
	// tolerates it and "the agent was not told" must stay distinguishable from
	// "there was nothing to tell".
	loose, err := e.HookPoll(ctx, "sess-1", "UserPromptSubmit", "", false, false)
	if err != nil {
		t.Fatalf("hook_poll: %v", err)
	}
	if len(loose) == 0 {
		t.Skip("no news for this agent, so there is no diagnosis to preserve")
	}
	var kept bool
	for k := range loose {
		if !allowed[k] {
			kept = true
		}
	}
	if !kept {
		t.Error("the non-strict response lost its diagnostic keys: strict mode was " +
			"supposed to narrow one caller's answer, not silently narrow everybody's")
	}
}

func keysOf(m core.Result) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
