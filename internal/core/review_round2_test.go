package core

import (
	"testing"
	"time"
)

// F2-1: a v0.0.6 adoption replays as it was folded: everything moves.
//
// The filter on the source watermark is a v0.0.7 security fix and stays for
// ops written under it. An adoption in a v0.0.6 ledger moved every message
// addressed to the source, the heir then answered one, and that answer is on
// disk; replaying the adoption with the filter skips the move and the answer
// folds to E_NO_MESSAGE. Found by the pre-release review, round two.
func TestAPreV7AdoptionMovesEverythingItMovedThen(t *testing.T) {
	s := NewState("test", DefaultLimits())
	t0 := time.Now()
	for _, o := range []*Op{
		{Kind: OpRegister, Name: "lost", NewToken: "tok-l", AgentKind: KindPersistent, Nonce: "nl"},
		{Kind: OpRegister, Name: "heir", NewToken: "tok-h", AgentKind: KindPersistent, Nonce: "nh"},
		{Kind: OpRegister, Name: "asker", NewToken: "tok-a", AgentKind: KindPersistent, Nonce: "na"},
	} {
		if _, _, err := s.Apply(o, t0); err != nil {
			t.Fatal("setup:", err)
		}
	}
	r, _, err := s.Apply(&Op{Kind: OpSendMessage, Token: "tok-a", To: "lost", MsgType: MsgQuestion, Body: "q"}, t0)
	if err != nil {
		t.Fatal("setup:", err)
	}
	q := r["msg_serial"].(uint64)
	// A historical sweep left the source's watermark ABOVE this question.
	s.Agents["lost"].TruncatedBefore = q + 1
	s.Agents["lost"].Status = StatusDormant
	if _, _, err := s.Apply(&Op{Kind: OpAdoptAgent, Token: "tok-h", To: "lost", AdoptAuthorised: true}, t0); err != nil {
		t.Fatal(err)
	}
	if s.Messages[q].To != "heir" {
		t.Fatalf("a v0.0.6 adoption did not move a message it moved at the time; the heir's "+
			"recorded answer to it now replays to E_NO_MESSAGE (To=%q)", s.Messages[q].To)
	}
}

// F2-4: mail the heir was given is visible whatever its own watermark says.
func TestAdoptedMailIsVisibleBelowTheHeirsWatermark(t *testing.T) {
	s := NewState("test", DefaultLimits())
	s.Agents["heir"] = &Agent{
		ID: "heir", Name: "heir", Status: StatusActive, TruncatedBefore: 100,
		Token: "tok-h", Slots: map[string]Slot{},
	}
	s.Messages[5] = &Message{
		Serial: 5, From: "asker", To: "heir", Type: MsgQuestion,
		State: MsgStatePending, AdoptedFrom: "lost",
	}
	s.Messages[6] = &Message{
		Serial: 6, From: "asker", To: "heir", Type: MsgQuestion,
		State: MsgStatePending, // a predecessor's, unmarked: stays fenced
	}
	got := map[uint64]bool{}
	for _, m := range s.Inbox("heir") {
		got[m.Serial] = true
	}
	if !got[5] {
		t.Error("the adopted message is hidden by the heir's watermark: adoption reported it " +
			"moved and the heir cannot see it")
	}
	if got[6] {
		t.Error("a predecessor's unmarked message became visible: the watermark stopped fencing")
	}
}

// F2-6: stating an alias the daemon had guessed confirms it.
func TestAResumeConfirmsAGuessedAlias(t *testing.T) {
	const alias = "01a0696b-8446-7821-a992-9dc7f6a43a25"
	s := NewState("test", DefaultLimits())
	t0 := time.Now()
	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "w", NewToken: "tok", AgentKind: KindPersistent,
		Nonce: "n", V7Semantics: true,
	}, t0); err != nil {
		t.Fatal("setup:", err)
	}
	l := s.Agents["w"]
	l.SessionAliases = []string{alias}
	l.GuessedSessions = []string{alias}
	if !l.GuessedSession(alias) {
		t.Fatal("setup: the alias is not marked guessed")
	}
	before := s.Serial
	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "w", Nonce: "n", NewToken: "tok2",
		SessionAlias: alias, SessionGuessed: false, V7Semantics: true,
	}, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if l.GuessedSession(alias) {
		t.Error("the owner named its alias outright and it is still marked guessed, so " +
			"anybody can reclaim it through mayClaimSession")
	}
	if s.Serial == before {
		t.Error("the confirmation changed state and did not advance the serial, so it is lost on restart")
	}
}
