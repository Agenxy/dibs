package core

import (
	"testing"
	"time"
)

// R3-1: the retention clamp never lowers the watermark below the fence that
// hides a previous occupant's mail.
//
// Registration raises TruncatedBefore past mail left for a reused id; the
// clamp added in round two lowered it to any remaining message addressed to
// that id, predecessor mail included. Found by the pre-release review, round
// three, with this recipe: retention 2, two retained predecessor messages, one
// new terminal message, sweep.
func TestTheClampNeverGoesBelowTheRegistrationFence(t *testing.T) {
	lim := DefaultLimits()
	lim.TerminalRetention = 2
	s := NewState("test", lim)
	// Two terminal messages left by the PREDECESSOR of id "seat".
	for _, ser := range []uint64{3, 4} {
		s.Messages[ser] = &Message{
			Serial: ser, From: "old-peer", To: "seat", Type: MsgNotify,
			State: MsgStateAcked, Body: "PREDECESSOR SECRET", Consumed: true,
			TerminalAt: time.Now(),
		}
	}
	// The reused id registers and is fenced above them.
	s.Agents["seat"] = &Agent{
		ID: "seat", Name: "seat", Status: StatusActive, Token: "tok",
		TruncatedBefore: 5, CreatedSerial: 5, Slots: map[string]Slot{},
	}
	// One new terminal message of its own, making three: retention evicts one.
	s.Messages[9] = &Message{
		Serial: 9, From: "peer", To: "seat", Type: MsgNotify,
		State: MsgStateAcked, Consumed: true, TerminalAt: time.Now(),
	}
	s.gc(time.Now(), false, true)
	if wm := s.Agents["seat"].TruncatedBefore; wm < 5 {
		t.Fatalf("the clamp lowered the watermark to %d, below the registration fence at 5: "+
			"the previous occupant's mail is readable by the agent that reused its name", wm)
	}
	for _, m := range s.Inbox("seat") {
		if m.Body == "PREDECESSOR SECRET" {
			t.Fatal("a previous occupant's message is in the inbox of the agent that reused its name")
		}
	}
}

// R3-2: a second adoption moves what the first one brought in.
func TestASecondAdoptionMovesAdoptedMail(t *testing.T) {
	s := NewState("test", DefaultLimits())
	s.Agents["heir1"] = &Agent{
		ID: "heir1", Name: "heir1", Status: StatusDormant, Token: "t1",
		TruncatedBefore: 100, Nonce: "n1", Slots: map[string]Slot{},
	}
	s.Agents["heir2"] = &Agent{
		ID: "heir2", Name: "heir2", Status: StatusActive, Token: "t2",
		Nonce: "n2", CreatedSerial: 200, Slots: map[string]Slot{},
	}
	// Adopted INTO heir1 earlier: below its watermark, marked, still pending.
	s.Messages[7] = &Message{
		Serial: 7, From: "asker", To: "heir1", Type: MsgQuestion,
		State: MsgStatePending, AdoptedFrom: "lost", AdoptedAt: 90,
	}
	if n := s.readdressMail(s.Agents["heir1"], s.Agents["heir2"], true); n != 1 {
		t.Fatalf("the second adoption moved %d message(s); the inbox it was judged by shows 1", n)
	}
	if s.Messages[7].To != "heir2" {
		t.Error("the adopted message did not move to the new heir")
	}
}

// R3-4: a ledgered resume records the contact durably.
func TestAResumeThatBindsIsADurableCheckpoint(t *testing.T) {
	const alias = "01a0696b-8446-7821-a992-9dc7f6a43a25"
	s := NewState("test", DefaultLimits())
	t0 := time.Now()
	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "w", NewToken: "tok", AgentKind: KindPersistent,
		Nonce: "n", V7Semantics: true,
	}, t0); err != nil {
		t.Fatal("setup:", err)
	}
	// INSIDE the TTL, so this lands on the live `resumed` branch the finding is
	// about. Forty minutes out took the dormant reattach branch, which has
	// always checkpointed, and the first draft of this test passed against
	// the bug for that reason. Asserted, so it cannot drift back.
	later := t0.Add(time.Minute)
	res, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "w", Nonce: "n", NewToken: "tok2",
		SessionAlias: alias, V7Semantics: true,
	}, later)
	if err != nil {
		t.Fatal(err)
	}
	if res["resumed"] != true {
		t.Fatalf("setup: this did not take the resumed branch (%v), so it tests the wrong path", res)
	}
	if got := s.Agents["w"].LastCoordination; !got.Equal(later) {
		t.Errorf("LastCoordination is %v after a ledgered resume at %v: a restart just past the "+
			"old TTL boots this agent stale despite a registration on disk seconds old", got, later)
	}
}
