// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestPriorityNotifyPrecedesNormalMailInOneWake(t *testing.T) {
	e, id := boardWithAgent(t)
	sender := registerAgent(t, e, "sender")
	for _, item := range []struct{ body, priority, kind string }{
		{"normal-first", "", core.MsgQuestion},
		{"high-second", "high", core.MsgNotify},
		{"urgent-third", "urgent", core.MsgNotify},
	} {
		res, err := e.Do(context.Background(), &core.Op{
			Kind: core.OpSendMessage, Token: sender, To: id,
			MsgType: item.kind, Body: item.body, RequestPriority: item.priority,
		})
		if err != nil || res["msg_serial"] == nil {
			t.Fatalf("send %s: %v %v", item.priority, res, err)
		}
	}
	if kind, _ := e.oldestBlocking(id); kind != core.MsgNotify {
		t.Fatalf("wake event chose older normal %s instead of priority notify", kind)
	}
	got, err := e.HookPoll(context.Background(), "sess-"+id, "Stop", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	carrier, _ := got["hookSpecificOutput"].(map[string]any)
	digest, _ := carrier["additionalContext"].(string)
	u, h, n := strings.Index(digest, "urgent-third"), strings.Index(digest, "high-second"), strings.Index(digest, "normal-first")
	if u < 0 || h < 0 || n < 0 || u >= h || h >= n {
		t.Fatalf("wake did not prioritize urgent/high notify: %q", digest)
	}
}

func TestPriorityNotifyDoesNotOverrideOperatorWakePhase(t *testing.T) {
	e, id := boardWithAgent(t)
	sender := registerAgent(t, e, "sender")
	e.SetWakePolicy(WakeUrgent)
	if _, err := e.Do(context.Background(), &core.Op{
		Kind: core.OpSendMessage, Token: sender, To: id,
		MsgType: core.MsgNotify, Body: "high alert", RequestPriority: "high",
	}); err != nil {
		t.Fatal(err)
	}
	quiet, err := e.HookPoll(context.Background(), "sess-"+id, "Stop", "", false, false)
	if err != nil || deliveredSomething(quiet) {
		t.Fatalf("priority overrode explicit operator wake phase: %v %v", quiet, err)
	}
	e.SetWakePolicy(WakeAll)
	fresh, err := e.HookPoll(context.Background(), "sess-"+id, "Stop", "", false, false)
	if err != nil || fresh["decision"] != "block" {
		t.Fatalf("suppressed priority alert was spent instead of retained: %v %v", fresh, err)
	}
}

// Every authored message reaches the agent without a person having to type,
// including a notify that asks for no reply. Generated updates are distinct.
func TestStopDeliversEveryAuthoredMessage(t *testing.T) {
	for _, kind := range []string{core.MsgNotify, core.MsgQuestion, core.MsgRequest, core.MsgHandoff} {
		t.Run(kind, func(t *testing.T) {
			e, id := boardWithAgent(t)
			ctx := context.Background()
			sender := registerAgent(t, e, "sender")
			if _, err := e.Do(ctx, &core.Op{
				Kind: core.OpSendMessage, Token: sender, To: id,
				MsgType: kind, Body: "something",
			}); err != nil {
				t.Fatalf("setup: send %s: %v", kind, err)
			}
			got, err := e.HookPoll(ctx, "sess-"+id, "Stop", "", false, false)
			if err != nil {
				t.Fatal(err)
			}
			if got["decision"] != "block" || got["hookSpecificOutput"] == nil {
				t.Errorf("actionable %s did not continue the turn: %v", kind, got)
			}
			if got["systemMessage"] == nil {
				t.Error("the human was not told")
			}
		})
	}
}

// It wakes ONCE. An agent that read something and chose not to act has
// exercised the judgement the digest explicitly grants it, and re-waking it
// every turn would be taking that back: nagging, which is the thing that
// deserved the name "driving the harness" all along.
func TestAWakeDoesNotNagAboutSomethingAlreadyDelivered(t *testing.T) {
	e, id := boardWithAgent(t)
	ctx := context.Background()
	sender := registerAgent(t, e, "sender")
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: sender, To: id,
		MsgType: core.MsgQuestion, Body: "an unanswered question",
	}); err != nil {
		t.Fatal(err)
	}

	first, err := e.HookPoll(ctx, "sess-"+id, "Stop", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if first["hookSpecificOutput"] == nil {
		t.Fatal("the arrival did not wake it: this test cannot see what it guards")
	}
	second, err := e.HookPoll(ctx, "sess-"+id, "Stop", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if second["hookSpecificOutput"] != nil {
		t.Error("the same question woke the agent twice inside the reminder cadence")
	}
	// The human keeps being told, because "unread" is still true and it costs
	// the agent nothing.
	if second["systemMessage"] == nil {
		t.Error("the human stopped being told as soon as the agent did")
	}
}

// Work somebody is BLOCKED on comes back. A question nobody has answered is not
// a decision, it is a peer waiting, and the point of a deadline is that
// somebody notices before it expires.
func TestBlockedWorkComesBackButAnFYIDoesNot(t *testing.T) {
	for _, tc := range []struct {
		kind   string
		expect bool
	}{
		{core.MsgQuestion, true},
		{core.MsgRequest, true},
		{core.MsgNotify, false},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			e, id := boardWithAgent(t)
			ctx := context.Background()
			sender := registerAgent(t, e, "sender")
			if _, err := e.Do(ctx, &core.Op{
				Kind: core.OpSendMessage, Token: sender, To: id,
				MsgType: tc.kind, Body: "waiting on you",
			}); err != nil {
				t.Fatal(err)
			}
			if !e.freshForWake(id, time.Now()) {
				t.Fatal("the arrival did not wake it")
			}
			if e.freshForWake(id, time.Now()) {
				t.Fatal("it woke twice in a row")
			}
			// A retry window later.
			later := time.Now().Add(AnnounceRetry + time.Second)
			if got := e.freshForWake(id, later); got != tc.expect {
				t.Errorf("after %s, a %s came back = %v, want %v",
					AnnounceRetry, tc.kind, got, tc.expect)
			}
		})
	}
}

// A wake must never continue a turn that a wake already continued.
//
// stop_hook_active is the harness saying "this turn is running because a stop
// hook asked for it". Continuing again is how a wake becomes a loop, and Claude
// Code caps it at eight before overriding: eight wasted turns rather than one.
// Not part of the policy, because it is a loop guard rather than a preference.
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
	again, err := e.HookPoll(ctx, "sess-"+id, "Stop", "", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if again["hookSpecificOutput"] != nil {
		t.Error("a turn already continued by a wake was continued again")
	}
	if again["systemMessage"] == nil {
		t.Error("the human stopped being told as soon as the model did")
	}
}

// The operator can trade awareness for tokens, deliberately, in both directions.
func TestTheOperatorCanNarrowOrSilenceTheWake(t *testing.T) {
	e, id := boardWithAgent(t)
	ctx := context.Background()
	sender := registerAgent(t, e, "sender")
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: sender, To: id,
		MsgType: core.MsgNotify, Body: "an FYI",
	}); err != nil {
		t.Fatal(err)
	}

	e.SetWakePolicy(WakeUrgent)
	if got, _ := e.HookPoll(ctx, "sess-"+id, "Stop", "", false, false); got["hookSpecificOutput"] != nil {
		t.Error("`urgent` extended a turn for an FYI")
	}
	e.SetWakePolicy(WakeNone)
	quiet, _ := e.HookPoll(ctx, "sess-"+id, "Stop", "", false, false)
	if quiet["hookSpecificOutput"] != nil {
		t.Error("`none` extended a turn")
	}
	if quiet["systemMessage"] == nil {
		t.Error("`none` stopped telling the human, which is not what it means")
	}
	// The default delivers authored FYIs. The operator can still silence
	// later blocking mail without consuming its presentation.
	fresh, freshID := boardWithAgent(t)
	s2 := registerAgent(t, fresh, "sender2")
	if _, err := fresh.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: s2, To: freshID, MsgType: core.MsgNotify, Body: "x",
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := fresh.HookPoll(ctx, "sess-"+freshID, "Stop", "", false, false); err != nil || got["decision"] != "block" {
		t.Fatalf("the default lost an authored FYI: %v %v", got, err)
	}
	if _, err := fresh.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: s2, To: freshID,
		MsgType: core.MsgQuestion, Body: "actionable",
	}); err != nil {
		t.Fatal(err)
	}
	fresh.SetWakePolicy(WakeNone)
	if got, err := fresh.HookPoll(ctx, "sess-"+freshID, "Stop", "", false, false); err != nil || deliveredSomething(got) {
		t.Fatalf("none spent an actionable wake: %v %v", got, err)
	}
	fresh.SetWakePolicy(WakeAll)
	if got, err := fresh.HookPoll(ctx, "sess-"+freshID, "Stop", "", false, false); err != nil || got["decision"] != "block" {
		t.Fatalf("default lost the actionable cause: %v %v", got, err)
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
