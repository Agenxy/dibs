package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestInlineHookDeliveryIsReadButSocketWriteIsNot(t *testing.T) {
	for _, event := range []string{"Stop", "SessionStart"} {
		t.Run(event, func(t *testing.T) {
			e := New(core.NewState("inline-hook", core.DefaultLimits()), &memLedger{}, deadProber{})
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			go e.Run(ctx)
			do := func(op *core.Op) core.Result {
				t.Helper()
				r, err := e.Do(ctx, op)
				if err != nil {
					t.Fatalf("setup %s: %v", op.Kind, err)
				}
				return r
			}
			tokens := map[string]string{}
			for _, id := range []string{"lead", "worker"} {
				tokens[id] = do(&core.Op{Kind: core.OpRegister, Name: id, SessionID: id + "-session", Nonce: "inline-hook-" + id})["token"].(string)
				do(&core.Op{Kind: core.OpAckBoard, Token: tokens[id]})
			}
			n := do(&core.Op{Kind: core.OpSendMessage, Token: tokens["lead"], To: "worker", MsgType: core.MsgRequest, Body: "work"})["msg_serial"].(uint64)
			do(&core.Op{Kind: core.OpRespond, Token: tokens["worker"], MsgSerial: n, Disposition: "approve", Body: "exact-inline-approval"})
			// The socket policy requires a real idle boundary, not silence after
			// fixture tool calls. An already-active Stop records it, not delivery.
			if _, err := e.HookPoll(ctx, "lead-session", "Stop", "", true, false); err != nil {
				t.Fatal(err)
			}
			offer, err := e.SocketOfferFor(ctx, tokens["lead"], "lead-session", "", false)
			if err != nil || !strings.Contains(fmt.Sprint(offer["digest"]), "exact-inline-approval") {
				t.Fatalf("socket offer: %v %v", offer, err)
			}
			if event == "Stop" {
				if _, err := e.SocketOfferFor(ctx, tokens["lead"], "lead-session", offer["offer"].(string), true); err != nil {
					t.Fatal(err)
				}
			}
			_, err = e.query(ctx, func() core.Result {
				if e.state.Messages[n].OutcomeReadAt != 0 {
					t.Error("socket write consumed unconfirmed mail")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			r, err := e.HookPoll(ctx, "lead-session", event, "", false, false)
			if err != nil || !strings.Contains(fmt.Sprint(r), "exact-inline-approval") {
				t.Fatalf("delivering hook: %v %v", r, err)
			}
			_, err = e.query(ctx, func() core.Result {
				if e.state.Messages[n].OutcomeReadAt == 0 {
					t.Error("delivering hook failed to ledger outcome read")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			r = do(&core.Op{Kind: core.OpAckBoard, Token: tokens["lead"]})
			if strings.Contains(fmt.Sprint(r["agent_updates"]), "APPROVED") {
				t.Errorf("read hook outcome repeated on pull: %v", r)
			}
		})
	}
}

func TestArchivedSessionMustReattachBeforeInlineDelivery(t *testing.T) {
	s := core.NewState("archived-inline", core.DefaultLimits())
	now := time.Now().Add(-time.Hour)
	apply := func(op *core.Op, at time.Time) core.Result {
		t.Helper()
		r, _, err := s.Apply(op, at)
		if err != nil {
			t.Fatalf("setup %s: %v", op.Kind, err)
		}
		return r
	}
	apply(&core.Op{Kind: core.OpInitializeReviewRead, ReviewReadCutoff: 1}, now)
	for _, id := range []string{"lead", "worker"} {
		apply(&core.Op{
			Kind: core.OpRegister, Name: id, NewToken: id + "-token",
			SessionID: id + "-session", Nonce: "archived-inline-" + id,
		}, now)
		apply(&core.Op{Kind: core.OpAckBoard, Token: id + "-token"}, now)
	}
	parent := apply(&core.Op{
		Kind: core.OpSendMessage, Token: "lead-token", To: "worker",
		MsgType: core.MsgQuestion, Body: "question", DeadlineSec: 7200,
	}, now)["msg_serial"].(uint64)
	stale := now.Add(s.Limits.AgentTTL + time.Minute)
	apply(&core.Op{Kind: core.OpSweep, StaleAgents: []string{"lead"}}, stale)
	apply(&core.Op{Kind: core.OpSweep}, stale.Add(s.Limits.StaleGrace+time.Minute))
	if s.Agents["lead"].Status != core.StatusArchived || s.AgentByToken("lead-token") != nil {
		t.Fatal("setup: real sweep did not archive and invalidate the token")
	}
	apply(&core.Op{
		Kind: core.OpRespond, Token: "worker-token", MsgSerial: parent,
		Disposition: "answer", Body: "archived-inline-answer",
	}, time.Now())
	e := New(s, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.Run(ctx)
	r, err := e.HookPoll(ctx, "lead-session", "SessionStart", "", false, false)
	if err != nil || strings.Contains(fmt.Sprint(r), "archived-inline-answer") {
		t.Fatalf("archived session received mail before reattachment: %v %v", r, err)
	}
	_, err = e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "lead",
		Nonce: "archived-inline-lead", SessionID: "lead-session",
	})
	if err != nil {
		t.Fatal("reattach:", err)
	}
	r, err = e.HookPoll(ctx, "lead-session", "SessionStart", "", false, false)
	if err != nil || !strings.Contains(fmt.Sprint(r), "archived-inline-answer") {
		t.Fatalf("reattached session could not deliver: %v %v", r, err)
	}
	r, err = e.HookPoll(ctx, "lead-session", "SessionStart", "", false, false)
	if err != nil || strings.Contains(fmt.Sprint(r), "archived-inline-answer") {
		t.Errorf("archived delivered outcome repeated: %v %v", r, err)
	}
}

func TestConfirmedSocketReadsOnlyTheFullyQuotedPrefix(t *testing.T) {
	e := New(core.NewState("socket-prefix", core.DefaultLimits()), &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.Run(ctx)
	do := func(op *core.Op) core.Result {
		t.Helper()
		r, err := e.Do(ctx, op)
		if err != nil {
			t.Fatalf("setup %s: %v", op.Kind, err)
		}
		return r
	}
	lead := do(&core.Op{
		Kind: core.OpRegister, Name: "lead", SessionID: "lead-session",
		Nonce: "socket-prefix-lead",
	})["token"].(string)
	worker := do(&core.Op{Kind: core.OpRegister, Name: "worker", Nonce: "socket-prefix-worker"})["token"].(string)
	do(&core.Op{Kind: core.OpAckBoard, Token: lead})
	parent := do(&core.Op{
		Kind: core.OpSendMessage, Token: lead, To: "worker",
		MsgType: core.MsgRequest, Body: "work",
	})["msg_serial"].(uint64)
	do(&core.Op{Kind: core.OpRespond, Token: worker, MsgSerial: parent, Disposition: "approve"})
	if _, err := e.GetMessage(ctx, lead, parent); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"complete-socket-report", strings.Repeat("λ", mailQuoteEach+1)} {
		do(&core.Op{Kind: core.OpRespond, Token: worker, MsgSerial: parent, Disposition: "progress", Body: body})
	}
	// Progress is informational. The existing socket policy needs independent
	// actionable mail before it may carry the shared digest's reports.
	do(&core.Op{
		Kind: core.OpSendMessage, Token: worker, To: "lead",
		MsgType: core.MsgQuestion, Body: "socket cause",
	})
	if _, err := e.HookPoll(ctx, "lead-session", "Stop", "", true, false); err != nil {
		t.Fatal(err)
	}
	offer, err := e.SocketOfferFor(ctx, lead, "lead-session", "", false)
	if err != nil || !strings.Contains(fmt.Sprint(offer["digest"]), "complete-socket-report") ||
		!strings.Contains(fmt.Sprint(offer["digest"]), "trimmed; read_mail") {
		t.Fatalf("setup: prefix offer %v %v", offer, err)
	}
	if _, err := e.SocketOfferFor(ctx, lead, "lead-session", offer["offer"].(string), true); err != nil {
		t.Fatal(err)
	}
	// This is an existing production lifecycle receipt, not a test setter or
	// an ordinary mid-turn call. UserPromptSubmit itself does not quote bodies.
	if _, err := e.HookPoll(ctx, "lead-session", "UserPromptSubmit", "", false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.query(ctx, func() core.Result {
		m := e.state.Messages[parent]
		if m.OutcomeReadAt != m.Progress[0].Serial {
			t.Error("confirmed socket did not read exact complete prefix, or read a trimmed body")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
