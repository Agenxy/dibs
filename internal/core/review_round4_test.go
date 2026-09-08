package core

import (
	"testing"
	"time"
)

// R4-1: a v0.0.6 claim does not refresh the checkpoint on replay.
//
// The refresh is right for new ops and rewrites history for old ones: a claim
// followed by a same-nonce register that reattached because the checkpoint was
// stale now replays that register as "still active", keeping the old token and
// allocating no serial, so every later serial disagrees with the ledger.
func TestAPreV7ClaimDoesNotRefreshTheCheckpoint(t *testing.T) {
	s := NewState("test", DefaultLimits())
	t0 := time.Now()
	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "lead", NewToken: "tok1",
		AgentKind: KindPersistent, Nonce: "n",
	}, t0); err != nil {
		t.Fatal("setup:", err)
	}
	stale := t0.Add(DefaultLimits().AgentTTL + time.Minute)
	if _, _, err := s.Apply(&Op{Kind: OpClaimCoordinator, Token: "tok1", ClaimVerified: true}, stale); err != nil {
		t.Fatal("setup:", err)
	}
	if !s.Agents["lead"].LastCoordination.Equal(t0) {
		t.Fatalf("a v0.0.6 claim refreshed the checkpoint to %v; the register that follows it "+
			"in a v0.0.6 ledger now replays as resumed instead of reattached", s.Agents["lead"].LastCoordination)
	}
	res, _, err := s.Apply(&Op{Kind: OpRegister, Name: "lead", Nonce: "n", NewToken: "tok2"}, stale.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if res["resumed"] == true || res["token"] != "tok2" {
		t.Errorf("the historical register replayed as resumed with the old token: %v", res)
	}
}

// R4-2: a v0.0.6 sweep does not hide, on replay, a question it never hid.
//
// Under v0.0.6 the watermark was inert. The readers that honour it arrived
// this cycle, and a v0.0.6 sweep that raised it past a pending question, with
// the clamp gated off for that sweep, left the question hidden from inbox and
// undelivered by check_in on every replay.
func TestAPreV7SweepDoesNotHideASurvivingQuestion(t *testing.T) {
	lim := DefaultLimits()
	lim.TerminalRetention = 1
	s := NewState("test", lim)
	t0 := time.Now()
	for _, o := range []*Op{
		{Kind: OpRegister, Name: "asker", NewToken: "tok-a", AgentKind: KindPersistent, Nonce: "na"},
		{Kind: OpRegister, Name: "busy", NewToken: "tok-b", AgentKind: KindPersistent, Nonce: "nb"},
		{Kind: OpAckBoard, Token: "tok-a"},
		{Kind: OpAckBoard, Token: "tok-b"},
	} {
		if _, _, err := s.Apply(o, t0); err != nil {
			t.Fatal("setup:", err)
		}
	}
	send := func(kind string) uint64 {
		r, _, err := s.Apply(&Op{Kind: OpSendMessage, Token: "tok-a", To: "busy", MsgType: kind, Body: "x"}, t0)
		if err != nil {
			t.Fatal("setup:", err)
		}
		return r["msg_serial"].(uint64)
	}
	q := send(MsgQuestion)
	n1, n2 := send(MsgNotify), send(MsgNotify)
	for _, ser := range []uint64{n1, n2} {
		if _, _, err := s.Apply(&Op{Kind: OpAckMessage, Token: "tok-b", MsgSerial: ser}, t0); err != nil {
			t.Fatal("setup:", err)
		}
	}
	if _, _, err := s.Apply(&Op{Kind: OpSweep}, t0.Add(time.Second)); err != nil { // a v0.0.6 sweep
		t.Fatal("setup:", err)
	}
	if s.Messages[n1] != nil {
		t.Fatal("setup: nothing was evicted, so the watermark was never raised")
	}
	found := false
	for _, m := range s.Inbox("busy") {
		found = found || m.Serial == q
	}
	if !found {
		t.Errorf("question %d is hidden behind watermark %d after a v0.0.6 sweep replayed: under "+
			"v0.0.6 that sweep hid nothing, and check_in delivered this question",
			q, s.Agents["busy"].TruncatedBefore)
	}
}

// R4-3: the adoption mark belongs to the incarnation that was given the mail.
func TestAReplacementDoesNotInheritThroughAnOldAdoption(t *testing.T) {
	s := NewState("test", DefaultLimits())
	// The predecessor "seat" (created at 10) was given this at serial 20, then purged.
	s.Messages[5] = &Message{
		Serial: 5, From: "asker", To: "seat", Type: MsgQuestion,
		State: MsgStatePending, Body: "PREDECESSOR MAIL", AdoptedFrom: "lost", AdoptedAt: 20,
	}
	// The replacement registers under the same id at 30, fenced above it.
	s.Agents["seat"] = &Agent{
		ID: "seat", Name: "seat", Status: StatusActive, Token: "tok",
		CreatedSerial: 30, TruncatedBefore: 25, Slots: map[string]Slot{},
	}
	for _, m := range s.Inbox("seat") {
		if m.Serial == 5 {
			t.Fatal("a replacement registered under a purged name read mail its predecessor " +
				"was given by adoption: the mark identified an adoption, not an owner")
		}
	}
	// The genuine heir (created at 15, adopted at 20) still reads it.
	s.Agents["seat"].CreatedSerial = 15
	found := false
	for _, m := range s.Inbox("seat") {
		found = found || m.Serial == 5
	}
	if !found {
		t.Error("the heir that was actually given this mail can no longer see it")
	}
}
