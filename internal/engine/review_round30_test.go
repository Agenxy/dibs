package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// The mailbox fence hides a predecessor's mail from a replacement that
// reuses its name, and read_mail refuses the body; ack and respond
// authorised on the reused id alone, so the replacement could acknowledge a
// notify it never saw, sending its sender a receipt, or answer a question
// by serial. Both are refused at ingress, so the acknowledgements already on
// disk replay as they were accepted.
func TestAReplacementCannotAckOrAnswerItsPredecessorsMail(t *testing.T) {
	now := time.Unix(1700000000, 0)
	st := core.NewState("t", core.DefaultLimits())
	apply := func(op *core.Op, at time.Time) core.Result {
		t.Helper()
		res, _, err := st.Apply(op, at)
		if err != nil {
			t.Fatal("setup:", err)
		}
		return res
	}
	apply(&core.Op{Kind: core.OpRegister, Name: "sender", NewToken: "tok-s", V7Semantics: true}, now)
	apply(&core.Op{Kind: core.OpRegister, Name: "target", NewToken: "tok-old", V7Semantics: true}, now)
	note := apply(&core.Op{Kind: core.OpSendMessage, Token: "tok-s", To: "target", MsgType: core.MsgNotify, Body: "for the previous occupant", V7Semantics: true}, now)
	ask := apply(&core.Op{Kind: core.OpSendMessage, Token: "tok-s", To: "target", MsgType: core.MsgQuestion, Body: "for the previous occupant", DeadlineSec: 3600, V7Semantics: true}, now)
	noteSerial, _ := note["msg_serial"].(uint64)
	askSerial, _ := ask["msg_serial"].(uint64)
	if noteSerial == 0 || askSerial == 0 {
		t.Fatalf("setup: the sends reported no serial: %v %v", note, ask)
	}
	// A sweep written before v0.0.7 removes the row and keeps the mail.
	delete(st.Agents, "target")
	apply(&core.Op{Kind: core.OpRegister, Name: "target", NewToken: "tok-new", V7Semantics: true}, now.Add(time.Hour))
	if got := st.Inbox("target"); len(got) != 0 {
		t.Fatalf("setup: the replacement sees %d of its predecessor's messages", len(got))
	}

	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	_, err := e.Do(ctx, &core.Op{Kind: core.OpAckMessage, Token: "tok-new", MsgSerial: noteSerial})
	var ce *core.Error
	if err == nil || !errors.As(err, &ce) || ce.Code != "E_NO_MESSAGE" {
		t.Fatalf("the replacement acked a notify it cannot see, and its sender got a receipt: %v", err)
	}
	if st.Messages[noteSerial].State != core.MsgStatePending {
		t.Fatalf("the predecessor's notify is %s after the refused ack", st.Messages[noteSerial].State)
	}
	_, err = e.Do(ctx, &core.Op{Kind: core.OpRespond, Token: "tok-new", MsgSerial: askSerial, Body: "not mine to answer"})
	if err == nil || !errors.As(err, &ce) || ce.Code != "E_NO_MESSAGE" {
		t.Fatalf("the replacement answered a question it cannot see: %v", err)
	}

	// And its own mail is still its own, or the fence refuses everything.
	own, err := e.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: "tok-s", To: "target", MsgType: core.MsgNotify, Body: "for you"})
	if err != nil {
		t.Fatal("setup:", err)
	}
	ownSerial, _ := own["msg_serial"].(uint64)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckMessage, Token: "tok-new", MsgSerial: ownSerial}); err != nil {
		t.Fatalf("the replacement cannot ack mail addressed to itself: %v", err)
	}
}
