package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// The OTHER door for unanswerable senders: the inbox tool.
//
// core's test asserts this for check_in and says in its own comment that BOTH
// read paths are covered. They are not: every assertion there reads the result
// of OpAckBoard, and Engine.Inbox is separate code in a separate package.
// Deleting the engine half would have left that test green while the tool an
// agent actually polls went silent again, which is the same two-doors mistake
// the release already made with the adoption note and with `messages` versus
// `inbox`. Found by the pre-release review, in the test whose comment claimed
// the coverage.
func TestTheInboxToolAlsoNamesUnanswerableSenders(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	mk := func(name string) string {
		r, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: name})
		if err != nil {
			t.Fatalf("setup %s: %v", name, err)
		}
		tok, _ := r["token"].(string)
		return tok
	}
	readerTok := mk("reader")
	ghostTok := mk("ghost")
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: readerTok}); err != nil {
		t.Fatal("setup ack:", err)
	}
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: ghostTok, To: "reader",
		MsgType: core.MsgNotify, Body: "thanks for the handover",
	}); err != nil {
		t.Fatal("setup send:", err)
	}

	// The sender goes away, through the real path rather than by poking state.
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpSignOff, Token: ghostTok}); err != nil {
		t.Fatal("setup sign_off:", err)
	}

	res, err := e.Inbox(ctx, readerTok)
	if err != nil {
		t.Fatal("inbox:", err)
	}
	// Setup must hold: there has to be mail here, or an absent warning proves
	// nothing at all.
	if mail, _ := res["messages"].([]*core.Message); len(mail) != 1 {
		t.Fatalf("setup: the reader has %d message(s), wanted the one from the "+
			"agent that has since gone", len(mail))
	}

	gone, _ := res["unanswerable_senders"].([]core.Result)
	if len(gone) != 1 {
		t.Fatalf("the inbox TOOL did not report the unanswerable sender (%v). "+
			"check_in reports it and this does not, so which answer an agent gets "+
			"depends on which call it happened to make", res["unanswerable_senders"])
	}
	if gone[0]["from"] != "ghost" {
		t.Errorf("the wrong sender was named: %v", gone[0])
	}
	if hint, _ := gone[0]["hint"].(string); hint == "" {
		t.Error("no hint, so the agent is told it cannot reply and not who to try")
	}
}
