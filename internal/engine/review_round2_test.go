package engine

import (
	"context"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// F2-2: the retention repair runs on the sweep the daemon actually performs.
//
// The clamp is gated on V7Semantics, and the engine builds its own sweep ops
// without passing exec, where that flag is stamped. So the repair existed, its
// test passed by calling gc directly, and no production sweep ever ran it.
// This drives e.sweep, the real path. Found by the pre-release review, round
// two.
func TestTheRealSweepDoesNotHideAPendingQuestion(t *testing.T) {
	lim := core.DefaultLimits()
	lim.TerminalRetention = 1
	st := core.NewState("test", lim)
	t0 := time.Now()
	apply := func(o *core.Op) core.Result {
		r, _, err := st.Apply(o, t0)
		if err != nil {
			t.Fatal("setup:", err)
		}
		return r
	}
	apply(&core.Op{Kind: core.OpRegister, Name: "asker", NewToken: "tok-a", AgentKind: core.KindPersistent, Nonce: "na", V7Semantics: true})
	apply(&core.Op{Kind: core.OpRegister, Name: "busy", NewToken: "tok-b", AgentKind: core.KindPersistent, Nonce: "nb", V7Semantics: true})
	apply(&core.Op{Kind: core.OpAckBoard, Token: "tok-a"})
	apply(&core.Op{Kind: core.OpAckBoard, Token: "tok-b"})
	q := apply(&core.Op{Kind: core.OpSendMessage, Token: "tok-a", To: "busy", MsgType: core.MsgQuestion, Body: "q", V7Semantics: true})["msg_serial"].(uint64)
	n1 := apply(&core.Op{Kind: core.OpSendMessage, Token: "tok-a", To: "busy", MsgType: core.MsgNotify, Body: "1", V7Semantics: true})["msg_serial"].(uint64)
	n2 := apply(&core.Op{Kind: core.OpSendMessage, Token: "tok-a", To: "busy", MsgType: core.MsgNotify, Body: "2", V7Semantics: true})["msg_serial"].(uint64)
	apply(&core.Op{Kind: core.OpAckMessage, Token: "tok-b", MsgSerial: n1})
	apply(&core.Op{Kind: core.OpAckMessage, Token: "tok-b", MsgSerial: n2})

	e := New(st, &memLedger{}, deadProber{})
	e.sweep(t0.Add(time.Second)) // the daemon's own sweep, not gc
	if st.Messages[n1] != nil {
		t.Fatal("setup: the real sweep evicted nothing, so the watermark never moved")
	}
	for _, m := range st.Inbox("busy") {
		if m.Serial == q {
			return
		}
	}
	t.Errorf("the daemon's own sweep hid a pending question (serial %d) behind the watermark (%d): "+
		"the repair is gated on a flag this op never carried", q, st.Agents["busy"].TruncatedBefore)
}

// F2-3: a replacement sender cannot read its predecessor's mail through the heir's adoption.
func TestAReplacementSenderCannotReadThroughAnAdoption(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	st.Agents["s"] = &core.Agent{
		ID: "s", Name: "s", Status: core.StatusActive, Token: "tok-s2",
		CreatedSerial: 50, Slots: map[string]core.Slot{}, // the REPLACEMENT, younger than the mail
	}
	st.Agents["h"] = &core.Agent{
		ID: "h", Name: "h", Status: core.StatusActive, Token: "tok-h",
		CreatedSerial: 60, Slots: map[string]core.Slot{},
	}
	st.Messages[7] = &core.Message{
		Serial: 7, From: "s", To: "h", Type: core.MsgQuestion,
		State: core.MsgStateAnswered, Body: "old sender's private question", Response: "and its answer",
		AdoptedFrom: "r",
	}
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	if res, err := e.GetMessage(ctx, "tok-s2", 7); err == nil && res["error"] == nil {
		t.Error("a replacement registered under the old sender's name read the old body and " +
			"answer by serial: the adoption authorised the recipient's recovery, not this")
	}
	if res, err := e.GetMessage(ctx, "tok-h", 7); err != nil || res["error"] != nil {
		t.Errorf("the heir itself was refused the mail it was given: %v %v", err, res)
	}
}

// F2-5: bind_session's takeover removes the id from the row that lost it.
func TestBindSessionTakeoverLeavesTheOldHolder(t *testing.T) {
	const thread = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	st := core.NewState("test", core.DefaultLimits())
	t0 := time.Now()
	for _, o := range []*core.Op{
		{Kind: core.OpRegister, Name: "old", NewToken: "tok-old", AgentKind: core.KindPersistent, Nonce: "no", SessionID: thread, V7Semantics: true},
		{Kind: core.OpRegister, Name: "new", NewToken: "tok-new", AgentKind: core.KindPersistent, Nonce: "nn", V7Semantics: true},
	} {
		if _, _, err := st.Apply(o, t0); err != nil {
			t.Fatal("setup:", err)
		}
	}
	st.Agents["old"].Status = core.StatusDormant
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpBindSession, Token: "tok-new", SessionID: thread}); err != nil {
		t.Fatal(err)
	}
	if !st.Agents["new"].HoldsSessionForTest(thread) {
		t.Fatal("the bind did not land, so this proves nothing")
	}
	if st.Agents["old"].HoldsSessionForTest(thread) {
		t.Error("the dormant holder still holds the session after bind_session took it: when it " +
			"returns, two active stated holders, and a hook resolves by id order")
	}
}

// read_mail on a serial that does not exist is an error, not a crash.
//
// The adoption exemption dereferenced the message before the ok check that
// guards it, and one read of a missing serial, which every agent does the
// moment it mistypes one, segfaulted the daemon. Every e2e that reads a wrong
// serial found it; no unit test had, because each read a message that existed.
func TestReadingAMissingSerialIsAnErrorNotACrash(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	st.Agents["r"] = &core.Agent{
		ID: "r", Name: "r", Status: core.StatusActive, Token: "tok-r",
		CreatedSerial: 3, Slots: map[string]core.Slot{},
	}
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	res, err := e.GetMessage(ctx, "tok-r", 999)
	if err == nil && (res == nil || res["error"] == nil) {
		t.Fatalf("a missing serial was reported as found: %v", res)
	}
}
