package core

import (
	"testing"
	"time"
)

// A v0.0.6 op must fold the way v0.0.6 folded it.
//
// Two repairs this cycle changed what an EXISTING op does: register began
// raising a new agent's watermark past mail its vanished predecessor left, and
// prune stopped re-closing a closed agent or advancing the serial for a no-op.
// Both are right for ops written from now on, and applying them to an older
// ledger reconstructs a board that never existed: a different inbox, and an
// `agent.closed` the original fold really did emit silently dropped, with the
// serial difference routed through the gap path that exists for corruption.
//
// The op records which semantics it was written under. This is the test that
// the recording is honoured, which is the half the first fix did not have.
// Found by the pre-release review, twice.
func TestAHistoricalRegisterDoesNotTruncateInheritedMail(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)
	reg(t, s, "sender", "tok-s", t0)
	reg(t, s, "target", "tok-t", t0)
	mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "tok-s", To: "target",
		MsgType: MsgNotify, Body: "left behind",
	}, now)
	delete(s.Agents, "target")

	// A register written by v0.0.6: no v7_semantics, which is how every op from
	// that version decodes.
	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "target", NewToken: "tok-t2",
	}, now); err != nil {
		t.Fatal(err)
	}
	if got := len(s.Inbox("target")); got != 1 {
		t.Errorf("replaying a v0.0.6 register produced an inbox of %d, not the 1 "+
			"that fold really produced. Replay must reconstruct the board that "+
			"existed, not the board today's rules would have made", got)
	}
}

// And a current one applies the repair.
func TestACurrentRegisterTruncatesInheritedMail(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)
	reg(t, s, "sender", "tok-s", t0)
	reg(t, s, "target", "tok-t", t0)
	mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "tok-s", To: "target",
		MsgType: MsgNotify, Body: "left behind",
	}, now)
	delete(s.Agents, "target")

	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "target", NewToken: "tok-t2", V7Semantics: true,
	}, now); err != nil {
		t.Fatal(err)
	}
	if got := len(s.Inbox("target")); got != 0 {
		t.Errorf("a new agent was handed %d message(s) belonging to whoever held "+
			"the id before it", got)
	}
}

// A v0.0.6 prune of an already-closed agent still closes it again and still
// advances the serial, because that is what its fold did.
func TestAHistoricalPruneKeepsItsOldSemantics(t *testing.T) {
	s := NewState("t", DefaultLimits())
	a := reg(t, s, "done", "tok-done", t0)
	mustApply(t, s, &Op{Kind: OpSignOff, Token: a.Token}, t0)
	before := s.Serial

	_, evs, err := s.Apply(&Op{Kind: OpPrune, To: "done"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	var closed bool
	for _, e := range evs {
		if e.Type == "agent.closed" {
			closed = true
		}
	}
	if !closed {
		t.Error("replaying a v0.0.6 prune dropped the agent.closed its fold really " +
			"emitted, so an upgrade silently rewrites the audit history")
	}
	if s.Serial == before {
		t.Error("replaying a v0.0.6 prune did not advance the serial it really " +
			"advanced, so the state falls behind the ledger and the difference is " +
			"repaired by the path that exists for corruption")
	}
}
