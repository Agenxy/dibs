package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestInlineSocketReceiptAfterOutcomePurged(t *testing.T) {
	led := &retentionLedger{}
	e := New(core.NewState("retention", core.DefaultLimits()), led, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	do := func(op *core.Op) core.Result {
		t.Helper()
		r, err := e.Do(ctx, op)
		if err != nil {
			t.Fatalf("setup %s: %v", op.Kind, err)
		}
		return r
	}
	lead := do(&core.Op{
		Kind: core.OpRegister, Name: "lead", Nonce: "late-receipt-lead",
		SessionID: "lead-session", AgentKind: core.KindPersistent,
	})["token"].(string)
	worker := do(&core.Op{Kind: core.OpRegister, Name: "worker", Nonce: "late-receipt-worker"})["token"].(string)
	do(&core.Op{Kind: core.OpAckBoard, Token: lead})
	parent := do(&core.Op{
		Kind: core.OpSendMessage, Token: lead, To: "worker", MsgType: core.MsgQuestion, Body: "proof",
	})["msg_serial"].(uint64)
	do(&core.Op{Kind: core.OpRespond, Token: worker, MsgSerial: parent, Disposition: "answer", Body: "offered answer"})
	survivor := do(&core.Op{
		Kind: core.OpSendMessage, Token: lead, To: "worker", MsgType: core.MsgRequest, Body: "ongoing work",
	})["msg_serial"].(uint64)
	do(&core.Op{
		Kind: core.OpRespond, Token: worker, MsgSerial: survivor, Disposition: "approve", Body: "surviving approval",
	})
	if _, err := e.HookPoll(ctx, "lead-session", "Stop", "", true, false); err != nil {
		t.Fatal(err)
	}
	offer, err := e.SocketOfferFor(ctx, lead, "lead-session", "", false)
	text := fmt.Sprint(offer["digest"])
	if err != nil || !strings.Contains(text, "offered answer") || !strings.Contains(text, "surviving approval") {
		t.Fatalf("setup: both outcomes must be quoted: %v %v", offer, err)
	}
	if _, err := e.SocketOfferFor(ctx, lead, "lead-session", offer["offer"].(string), true); err != nil {
		t.Fatal(err)
	}
	var records int
	_, err = e.query(ctx, func() core.Result {
		if e.state.Messages[parent].OutcomeReadAt != 0 || e.state.Messages[survivor].OutcomeReadAt != 0 {
			return core.Result{"error": fmt.Errorf("setup: a socket write alone consumed an outcome")}
		}
		// Drive the real GC fold with a recorded future time, not a deletion
		// or a sleep. Accepted work survives; the answered question expires.
		_, sweepErr := e.applyAndLedger(&core.Op{
			Kind: core.OpSweep, PurgeMail: true, V7Semantics: true, KeepOwed: true, KeepArchivedNonce: true,
		}, time.Now().Add(26*time.Hour))
		if sweepErr != nil {
			return core.Result{"error": sweepErr}
		}
		if e.state.Messages[parent] != nil || e.state.Messages[survivor] == nil {
			return core.Result{"error": fmt.Errorf("setup: production sweep did not purge only the completed outcome")}
		}
		records = len(led.records)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.HookPoll(ctx, "lead-session", "UserPromptSubmit", "", false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.query(ctx, func() core.Result {
		if e.state.Messages[parent] != nil || len(e.socketOffers) != 0 {
			t.Error("late confirmation resurrected mail or retained a settled offer")
		}
		m := e.state.Messages[survivor]
		if m.OutcomeReadAt != m.LatestOutcomeSerial() {
			t.Error("surviving fully quoted outcome was not durably read")
		}
		reads := 0
		for _, record := range led.records[records:] {
			var op core.Op
			if err := json.Unmarshal(record.op, &op); err != nil {
				return core.Result{"error": err}
			}
			if op.Kind == core.OpOutcomeRead {
				reads++
				if op.MsgSerial != survivor || op.OutcomeThroughSerial != m.OutcomeReadAt {
					t.Error("late receipt recorded a phantom or unquoted read")
				}
			}
		}
		if reads != 1 {
			t.Errorf("late receipt recorded %d reads, want the one surviving quote", reads)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	(&retentionBoard{t: t, e: e, ctx: ctx, led: led}).assertReplay()
}
