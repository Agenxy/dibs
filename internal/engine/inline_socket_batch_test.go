package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

func TestSocketBatchSharesMailFirstBudgetAndReadsEachQuotedParticipant(t *testing.T) {
	e := New(core.NewState("inline-batch", core.DefaultLimits()), &memLedger{}, deadProber{})
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
	parents := map[string]uint64{}
	for _, id := range []string{"lead-a", "lead-b", "lead-c", "worker"} {
		session := "shared-session"
		if id == "worker" {
			session = "worker-session"
		}
		tokens[id] = do(&core.Op{
			Kind: core.OpRegister, Name: id, SessionID: session,
			Nonce: "inline-batch-" + id,
		})["token"].(string)
		do(&core.Op{Kind: core.OpAckBoard, Token: tokens[id]})
	}
	for _, id := range []string{"lead-a", "lead-b", "lead-c"} {
		parents[id] = do(&core.Op{
			Kind: core.OpSendMessage, Token: tokens[id], To: "worker",
			MsgType: core.MsgRequest, Body: "work",
		})["msg_serial"].(uint64)
		do(&core.Op{
			Kind: core.OpRespond, Token: tokens["worker"], MsgSerial: parents[id],
			Disposition: "approve", Body: strings.Repeat("λ", 700),
		})
	}
	// The oldest registered holder runs the hooks; mail addressed to another
	// authenticated holder still receives the shared budget BEFORE outcomes.
	do(&core.Op{
		Kind: core.OpSendMessage, Token: tokens["worker"], To: "lead-b",
		MsgType: core.MsgQuestion, Body: strings.Repeat("μ", 700),
	})
	if _, err := e.HookPoll(ctx, "shared-session", "Stop", "", true, false); err != nil {
		t.Fatal(err)
	}
	offer, err := e.SocketOffersFor(ctx, []string{tokens["lead-a"], tokens["lead-b"], tokens["lead-c"]},
		"shared-session", "", false)
	text := fmt.Sprint(offer["digest"])
	if err != nil || !strings.Contains(text, strings.Repeat("μ", 700)) {
		t.Fatalf("setup: mail absent from authenticated batch: %v %v", offer, err)
	}
	if strings.Count(text, "λ") != 900 {
		t.Errorf("batch did not share 1600-rune mail-first budget: λ=%d", strings.Count(text, "λ"))
	}
	if _, err := e.SocketOffersFor(ctx, []string{tokens["lead-a"], tokens["lead-b"], tokens["lead-c"]},
		"shared-session", offer["offer"].(string), true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.query(ctx, func() core.Result {
		for _, parent := range parents {
			if e.state.Messages[parent].OutcomeReadAt != 0 {
				t.Error("batch socket write read an unconfirmed outcome")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.HookPoll(ctx, "shared-session", "UserPromptSubmit", "", false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.query(ctx, func() core.Result {
		for id, parent := range parents {
			read := e.state.Messages[parent].OutcomeReadAt
			// Newest request C is complete; B is partial and A only a pointer.
			if (id == "lead-c" && read == 0) || (id != "lead-c" && read != 0) {
				t.Errorf("wrong participant prefix read: %s read=%d", id, read)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSocketBatchCountBoundLeavesUnshownParticipantOutcomeUnread(t *testing.T) {
	e := New(core.NewState("batch-count", core.DefaultLimits()), &memLedger{}, deadProber{})
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
	for _, id := range []string{"lead-a", "lead-b", "worker"} {
		session := "shared-session"
		if id == "worker" {
			session = "worker-session"
		}
		tokens[id] = do(&core.Op{
			Kind: core.OpRegister, Name: id, SessionID: session,
			Nonce: "batch-count-" + id,
		})["token"].(string)
		do(&core.Op{Kind: core.OpAckBoard, Token: tokens[id]})
	}
	var oldest uint64
	for i := range maxInlineOutcomes + 1 {
		id := "lead-a"
		if i%2 == 1 {
			id = "lead-b"
		}
		n := do(&core.Op{
			Kind: core.OpSendMessage, Token: tokens[id], To: "worker",
			MsgType: core.MsgQuestion, Body: "question",
		})["msg_serial"].(uint64)
		if i == 0 {
			oldest = n
		}
		do(&core.Op{
			Kind: core.OpRespond, Token: tokens["worker"], MsgSerial: n,
			Disposition: "answer", Body: fmt.Sprintf("answer-%02d", i),
		})
	}
	if _, err := e.HookPoll(ctx, "shared-session", "Stop", "", true, false); err != nil {
		t.Fatal(err)
	}
	offer, err := e.SocketOffersFor(ctx, []string{tokens["lead-a"], tokens["lead-b"]},
		"shared-session", "", false)
	text := fmt.Sprint(offer["digest"])
	if err != nil || strings.Count(text, "answered your question") != maxInlineOutcomes ||
		strings.Contains(text, "answer-00") {
		t.Fatalf("batch count bound/newest-first order: %v %v", offer, err)
	}
	if _, err := e.SocketOffersFor(ctx, []string{tokens["lead-a"], tokens["lead-b"]},
		"shared-session", offer["offer"].(string), true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.HookPoll(ctx, "shared-session", "UserPromptSubmit", "", false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.query(ctx, func() core.Result {
		if e.state.Messages[oldest].OutcomeReadAt != 0 {
			t.Error("batch count bound consumed unshown oldest outcome")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
