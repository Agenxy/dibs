package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A wake must not stop an agent from stopping unless somebody is waiting on it.
//
// On Stop, `additionalContext` is not merely informative: Claude Code's own
// documentation says it "keeps the conversation going", through the same loop
// protections as a blocking decision and an eight-continuation cap. So every
// piece of mail was extending a finished turn, a plain FYI included. That is
// Dibs driving a harness, which PHILOSOPHY.md rule 5 forbids and which the wake
// path exists specifically not to do. Reported by the operator, who saw the
// notice and asked the right question: does this stop the agents?
//
// The urgency is not guessed. The sender chose a type, and the types already
// mean exactly this.
func TestOnlyWorkSomebodyIsWaitingOnExtendsATurn(t *testing.T) {
	for _, tc := range []struct {
		name         string
		msg          string
		wantOnStop   bool
		wantOnPrompt bool
	}{
		{"a question blocks its sender", core.MsgQuestion, true, true},
		{"a request blocks its sender", core.MsgRequest, true, true},
		{"a handoff is work nobody is doing", core.MsgHandoff, true, true},
		{"an FYI waits for the next activation", core.MsgNotify, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, id := boardWithAgent(t)
			ctx := context.Background()
			sender := registerAgent(t, e, "sender")
			if _, err := e.Do(ctx, &core.Op{
				Kind: core.OpSendMessage, Token: sender, To: id,
				MsgType: tc.msg, Body: "something",
			}); err != nil {
				t.Fatalf("setup: send %s: %v", tc.msg, err)
			}

			stop, err := e.HookPoll(ctx, "sess-"+id, "Stop", "", false)
			if err != nil {
				t.Fatal(err)
			}
			if got := stop["hookSpecificOutput"] != nil; got != tc.wantOnStop {
				t.Errorf("Stop extended the turn = %v, want %v. A %s %s",
					got, tc.wantOnStop, tc.msg,
					map[bool]string{true: "must reach the agent now", false: "must not extend a finished turn"}[tc.wantOnStop])
			}
			// The human is told either way: "your agent has mail it is not
			// stopping for" is exactly what they want to know.
			if stop["systemMessage"] == nil {
				t.Error("the human was not told, whatever was decided about the model")
			}

			// A prompt boundary interrupts nothing, so everything lands there.
			prompt, err := e.HookPoll(ctx, "sess-"+id, "UserPromptSubmit", "", false)
			if err != nil {
				t.Fatal(err)
			}
			if got := prompt["hookSpecificOutput"] != nil; got != tc.wantOnPrompt {
				t.Errorf("UserPromptSubmit delivered = %v, want %v: that event is already a "+
					"boundary and interrupts nothing", got, tc.wantOnPrompt)
			}
		})
	}
}

// A wake must never continue a turn that a wake already continued.
//
// stop_hook_active is the harness saying "this turn is running because a stop
// hook asked for it". Continuing again on the same unread mail is how a wake
// becomes a loop, and Claude Code caps it at eight before overriding: eight
// wasted turns rather than one.
func TestAWakeNeverContinuesATurnAWakeAlreadyContinued(t *testing.T) {
	e, id := boardWithAgent(t)
	ctx := context.Background()
	sender := registerAgent(t, e, "sender")
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: sender, To: id,
		MsgType: core.MsgQuestion, Body: "are you there?",
	}); err != nil {
		t.Fatal(err)
	}

	first, err := e.HookPoll(ctx, "sess-"+id, "Stop", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if first["hookSpecificOutput"] == nil {
		t.Fatal("a question did not reach the agent at all: this test cannot see what it guards")
	}
	again, err := e.HookPoll(ctx, "sess-"+id, "Stop", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if again["hookSpecificOutput"] != nil {
		t.Error("the turn was continued again while stop_hook_active was set: unread mail " +
			"that the agent has not dealt with would extend every turn until the harness " +
			"overrides at eight")
	}
	if again["systemMessage"] == nil {
		t.Error("the human stopped being told as soon as the model did")
	}
}

// boardWithAgent returns a running engine and one registered agent bound to a
// session, which is what a lifecycle hook resolves.
func boardWithAgent(t *testing.T) (*Engine, string) {
	t.Helper()
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.Run(ctx)

	res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "worker", SessionID: "sess-worker"})
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := res["token"].(string)
	id, _ := res["agent_id"].(string)
	if tok == "" || id == "" {
		t.Fatal("setup: register returned nothing usable")
	}
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok}); err != nil {
		t.Fatal(err)
	}
	_ = tok
	return e, id
}

func registerAgent(t *testing.T, e *Engine, name string) string {
	t.Helper()
	ctx := context.Background()
	res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: name})
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := res["token"].(string)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok}); err != nil {
		t.Fatal(err)
	}
	return tok
}

// An unattended fleet can choose to be woken for everything.
//
// The default holds an FYI until the agent's next activation, and an agent
// nobody prompts may not have one for hours: on a machine running without a
// person, the queue is where mail waits. That is the operator's call about
// their own fleet, not something a default can know, so it is one config key.
//
// The loop guard is NOT part of the policy. stop_hook_active means this turn is
// already running because a wake continued it, and continuing again is a loop
// whatever the operator prefers.
func TestAnUnattendedFleetCanBeWokenForEverything(t *testing.T) {
	e, id := boardWithAgent(t)
	ctx := context.Background()
	sender := registerAgent(t, e, "sender")
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: sender, To: id,
		MsgType: core.MsgNotify, Body: "an FYI",
	}); err != nil {
		t.Fatal(err)
	}

	if got, _ := e.HookPoll(ctx, "sess-"+id, "Stop", "", false); got["hookSpecificOutput"] != nil {
		t.Fatal("the default extended a turn for an FYI: this test cannot see what it guards")
	}

	e.SetWakePolicy(WakeAll)
	got, err := e.HookPoll(ctx, "sess-"+id, "Stop", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if got["hookSpecificOutput"] == nil {
		t.Error("`all` did not deliver an FYI on Stop, so an unattended fleet has no way " +
			"to be woken by one")
	}
	// Still never twice in a row: a preference cannot switch off a loop guard.
	if again, _ := e.HookPoll(ctx, "sess-"+id, "Stop", "", true); again["hookSpecificOutput"] != nil {
		t.Error("`all` continued a turn that a wake had already continued: the eight-" +
			"continuation cap is the only thing left stopping the loop")
	}

	// And `none` is strictly pull-shaped, with the human still told.
	e.SetWakePolicy(WakeNone)
	quiet, _ := e.HookPoll(ctx, "sess-"+id, "Stop", "", false)
	if quiet["hookSpecificOutput"] != nil {
		t.Error("`none` still extended a turn")
	}
	if quiet["systemMessage"] == nil {
		t.Error("`none` stopped telling the human, which is not what it means")
	}
}
