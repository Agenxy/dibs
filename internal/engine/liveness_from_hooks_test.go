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

// Dibs decides liveness from its own evidence, and stops asking the agent.
//
// THE DAEMON HELD THE PROOF AND COMPLAINED ANYWAY. A harness lifecycle hook
// firing means that session exists and just took a turn. The daemon recorded
// those hooks, and then swept the row stale on a different clock and delivered,
// ON THAT SAME HOOK, a line reading "you have not coordinated with the board
// for 9h33m: your declaration reads stale and peers writing to you may be told
// you are dormant. check_in now". The operator read it off their own screen and
// asked why an agent has to keep announcing something the board can see.
//
// Three clocks existed and were read in different combinations: LastCoordination
// (durable, checkpointed once per AgentTTL/2, so routinely minutes stale on a
// healthy agent), `seen` (ephemeral, and deliberately NOT stamped by a finishing
// hook because it also answers "would a wake collide with a running turn"), and
// nothing at all for "a hook fired". Now there is a fourth that answers only the
// liveness question, every hook stamps it, and one helper reads all of them.
func TestAnAgentWhoseHooksFireIsNotSweptDormant(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "worker"})
	if err != nil {
		t.Fatal("setup:", err)
	}
	tok, _ := res["token"].(string)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok}); err != nil {
		t.Fatal("setup:", err)
	}
	const sid = "worker-session"
	if _, err := e.BindSession(ctx, tok, sid); err != nil {
		t.Fatal("setup:", err)
	}

	// Nine hours since it last wrote to the ledger, and every ephemeral clock
	// aged with it: this is what a genuinely silent agent looks like.
	silence := func() {
		_, _ = e.query(ctx, func() core.Result {
			old := time.Now().Add(-9 * time.Hour)
			e.state.Agents["worker"].LastCoordination = old
			e.seen["worker"] = old
			delete(e.hookAlive, "worker")
			return core.Result{}
		})
	}
	silence()
	_, _ = e.query(ctx, func() core.Result {
		if now := time.Now(); now.Sub(e.lastEvidenceOf(e.state.Agents["worker"])) < 8*time.Hour {
			t.Error("setup: the agent does not read as silent, so the hook below proves nothing")
		}
		return core.Result{}
	})

	// A Stop hook. It is the END of a turn, so it must not read as "busy", and
	// it is proof the session is there, so it must count as liveness.
	if _, err := e.HookPoll(ctx, sid, "Stop", "", false, false); err != nil {
		t.Fatal("hook_poll:", err)
	}
	_, _ = e.query(ctx, func() core.Result {
		l := e.state.Agents["worker"]
		if quiet := time.Since(e.lastEvidenceOf(l)); quiet > time.Minute {
			t.Errorf("after a Stop hook the daemon still believes it has not seen this "+
				"agent for %s. The hook IS the evidence: it arrived from the session, on "+
				"the connection the model already holds", roughDur(quiet))
		}
		// And the recency clock is untouched, or a finished turn would look
		// like a running one and the wake it is owed would be skipped.
		if s, ok := e.seen["worker"]; ok && time.Since(s) < time.Hour {
			t.Error("a finishing hook stamped the mid-turn clock. That clock decides " +
				"whether a wake would collide with a running turn, and a Stop means the " +
				"turn is over, which is exactly when a wake should go")
		}
		return core.Result{}
	})

	// The sweep, which is what peers actually see, must agree.
	_, _ = e.query(ctx, func() core.Result {
		e.sweep(time.Now())
		return core.Result{}
	})
	_, _ = e.query(ctx, func() core.Result {
		if l := e.state.Agents["worker"]; l.Status == core.StatusDormant {
			t.Errorf("an agent whose hook fired seconds ago was swept %s (%q). Peers "+
				"writing to it are then told it is dormant, which is the report that "+
				"made this look like a product failure", l.Status, l.StaleReason)
		}
		return core.Result{}
	})
}

// And the digest says nothing about the agent's own silence any more.
//
// The reminder is gone rather than reworded. Its only delivery route was a
// hook, so it could only reach agents whose liveness the daemon can now prove,
// and its claim that peers may be told they are dormant is false for exactly
// that population. A reminder that cannot be true for anyone who can receive it
// is not a reminder.
func TestTheDigestDoesNotAskALiveAgentToAnnounceItself(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	reg := func(name string) string {
		r, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: name})
		if err != nil {
			t.Fatal("setup:", err)
		}
		tok, _ := r["token"].(string)
		if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok}); err != nil {
			t.Fatal("setup:", err)
		}
		return tok
	}
	quiet, peer := reg("quiet"), reg("peer")
	const sid = "quiet-session"
	if _, err := e.BindSession(ctx, quiet, sid); err != nil {
		t.Fatal("setup:", err)
	}
	_, _ = e.query(ctx, func() core.Result {
		e.state.Agents["quiet"].LastCoordination = time.Now().Add(-9 * time.Hour)
		return core.Result{}
	})
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: peer, To: "quiet", MsgType: core.MsgQuestion, Body: "liveness-proof-question",
	}); err != nil {
		t.Fatal("setup:", err)
	}

	res, err := e.HookPoll(ctx, sid, "Stop", "", false, false)
	if err != nil {
		t.Fatal("hook_poll:", err)
	}
	whole := res["systemMessage"]
	if hs, ok := res["hookSpecificOutput"].(map[string]any); ok {
		whole = whole.(string) + " " + hs["additionalContext"].(string)
	}
	text, _ := whole.(string)
	if text == "" {
		t.Fatal("setup: the digest was empty, so it cannot show the absence of anything")
	}
	for _, banned := range []string{"not coordinated", "check_in now", "reads stale"} {
		if strings.Contains(text, banned) {
			t.Errorf("the digest still asks a live agent to announce itself (%q): %s.\n"+
				"  The hook that delivered this is the proof the agent is there",
				banned, text)
		}
	}
	// The mail is still delivered: this removed a nag, not a notification.
	if !strings.Contains(text, "liveness-proof-question") {
		t.Errorf("the mail went missing along with the reminder: %s", text)
	}
}

// roughDur is a duration for a test failure message.
func roughDur(d time.Duration) string { return d.Round(time.Minute).String() }
