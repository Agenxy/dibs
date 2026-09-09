package core

import (
	"strings"
	"testing"
	"time"
)

// YOU CAN STILL WRITE TO AN AGENT THE SWEEP ARCHIVED.
//
// Answerable asked Gone(), which is Closed || Archived, so applySend refused a
// send to an archived agent with E_NO_AGENT: "no live agent". Closed is right
// and stays: that agent decided to stop. Archived is a TIMER, and for an
// ephemeral agent it is AgentTTL + StaleGrace, thirty-five minutes on the
// defaults.
//
// The row is kept for ArchiveRetention, seven whole days, precisely so the
// agent can come back: TestAnArchivedAgentComesBackWithItsNonceAndItsMail
// pins that recovery, and E_BAD_TOKEN's hint is the instruction for it. So for
// those seven days the board held an identity it could restore, a mailbox it
// would hand back intact, and a refusal to put anything new in it. The one
// thing an agent could not do with a recoverable peer was reach it.
//
// NO GATE, AND THIS IS THE REASON. AGENTS.md's rule is that changed fold
// BEHAVIOUR must be recorded on the op, because replay applies today's Apply
// to yesterday's ops. That hazard needs the old op to EXIST. This refusal
// returned an error, an op that errors never advanced the serial and was never
// ledgered, so no ledger anywhere contains a send to an archived agent. There
// is no past for this to be retroactive about. Relaxing a refusal is the one
// shape of fold change that is safe unguarded, and only while that stays true:
// anything that changes what an ACCEPTED op did still needs the flag.
func TestMailCanBeSentToAnAgentTheSweepArchived(t *testing.T) {
	s := NewState("test", DefaultLimits())
	now := time.Unix(1700000000, 0)

	res, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "worker", NewToken: "tok-1", Nonce: "n-keepme",
	}, now)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	id, _ := res["agent_id"].(string)
	if id == "" {
		t.Fatalf("setup: no agent_id in %v", res)
	}
	if _, _, err := s.Apply(&Op{Kind: OpRegister, Name: "peer", NewToken: "tok-peer"}, now); err != nil {
		t.Fatalf("register peer: %v", err)
	}
	if _, _, err := s.Apply(&Op{Kind: OpAckBoard, Token: "tok-peer"}, now); err != nil {
		t.Fatalf("peer ack: %v", err)
	}

	// Silence, then the two sweeps that carry an ephemeral agent to archived.
	stale := now.Add(s.Limits.AgentTTL + time.Minute)
	if _, _, err := s.Apply(&Op{Kind: OpSweep, StaleAgents: []string{id}}, stale); err != nil {
		t.Fatalf("sweep to stale: %v", err)
	}
	archived := stale.Add(s.Limits.StaleGrace + time.Minute)
	if _, _, err := s.Apply(&Op{Kind: OpSweep}, archived); err != nil {
		t.Fatalf("sweep to archived: %v", err)
	}
	if got := s.Agents[id].Status; got != StatusArchived {
		t.Fatalf("setup did not reach the state under test: status is %q, want archived", got)
	}

	sent, _, err := s.Apply(&Op{
		Kind: OpSendMessage, Token: "tok-peer", To: id,
		MsgType: MsgQuestion, Text: "are you there",
	}, archived.Add(time.Minute))
	if err != nil {
		t.Fatalf("send to an archived agent was refused: %v\n"+
			"its row, its mailbox and its nonce all live for another %s, so this is "+
			"an identity the board can restore and would not let anyone write to",
			err, s.Limits.ArchiveRetention)
	}
	if sent["msg_serial"] == nil {
		t.Errorf("send returned no serial: %v", sent)
	}
	if in := s.Inbox(id); len(in) != 1 {
		t.Errorf("archived agent's inbox has %d messages, want 1", len(in))
	}

	// AND THE SENDER IS TOLD WHAT IT JUST BOUGHT.
	//
	// Accepting the send silently would be the worse half of the old bug rather
	// than a fix: the sender would read {"ok": true} and wait, on a mailbox whose
	// owner has to re-register before it can read anything. sleepingNote covered
	// stale and dormant and returned "" for archived, because archived could not
	// receive mail when it was written.
	note, _ := sent["note"].(string)
	if note == "" {
		t.Fatal("send to an archived agent returned no note: the sender was told " +
			"nothing that distinguishes this from delivery to a working agent, and " +
			"the recipient cannot read it without registering again")
	}
	for _, want := range []string{"ARCHIVED", "nonce", s.Limits.ArchiveRetention.String()} {
		if !strings.Contains(note, want) {
			t.Errorf("the note does not mention %q, which is the part the sender acts "+
				"on:\n  %s", want, note)
		}
	}
}

// The half that must not move: a deliberate sign_off is still a closed door,
// and the refusal still names the live agents instead of saying "check the
// board".
func TestMailToAnAgentThatSignedOffIsStillRefused(t *testing.T) {
	s := NewState("test", DefaultLimits())
	now := time.Unix(1700000000, 0)

	res, _, err := s.Apply(&Op{Kind: OpRegister, Name: "worker", NewToken: "tok-1"}, now)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	id, _ := res["agent_id"].(string)
	if _, _, err := s.Apply(&Op{Kind: OpAckBoard, Token: "tok-1"}, now); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if _, _, err := s.Apply(&Op{Kind: OpSignOff, Token: "tok-1"}, now); err != nil {
		t.Fatalf("sign off: %v", err)
	}
	if _, _, err := s.Apply(&Op{Kind: OpRegister, Name: "peer", NewToken: "tok-peer"}, now); err != nil {
		t.Fatalf("register peer: %v", err)
	}
	if _, _, err := s.Apply(&Op{Kind: OpAckBoard, Token: "tok-peer"}, now); err != nil {
		t.Fatalf("peer ack: %v", err)
	}

	_, _, err = s.Apply(&Op{
		Kind: OpSendMessage, Token: "tok-peer", To: id,
		MsgType: MsgNotify, Text: "hello",
	}, now.Add(time.Minute))
	if err == nil {
		t.Fatal("mail was accepted for an agent that signed off: sign_off is final, " +
			"and delivering to it would leave the sender waiting on a decision the " +
			"agent already made")
	}
	if !strings.Contains(err.Error(), "no live agent") {
		t.Errorf("refusal does not name the case: %v", err)
	}
}

// A sweep from before the flag existed still clears the nonce.
//
// The change is to what an ALREADY LEDGERED op kind does, which is the hazard
// AGENTS.md names and this repository has been caught by four times: replay
// runs today's fold over yesterday's ops, so a v0.0.7 sweep replayed with the
// new behaviour would reconstruct a board holding a credential the original
// fold destroyed. The daemon would then diverge from its own history at the
// first thing that reads it.
//
// Every historical sweep looks exactly like the one below: no flag at all.
func TestASweepWrittenBeforeTheFlagStillClearsTheNonce(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)

	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "worker", NewToken: "tok-1", Nonce: "n-keepme",
	}, now); err != nil {
		t.Fatalf("register: %v", err)
	}
	id := s.Nonces["n-keepme"]
	if id == "" {
		t.Fatal("setup: the nonce index does not resolve")
	}

	stale := now.Add(s.Limits.AgentTTL + time.Minute)
	mustApply(t, s, &Op{Kind: OpSweep, StaleAgents: []string{id}}, stale)
	// The historical sweep: no KeepArchivedNonce, as every sweep on disk today.
	mustApply(t, s, &Op{Kind: OpSweep}, stale.Add(s.Limits.StaleGrace+time.Minute))

	if got := s.Agents[id].Status; got != StatusArchived {
		t.Fatalf("setup did not reach the state under test: status %q", got)
	}
	if n := s.Agents[id].Nonce; n != "" {
		t.Errorf("an unflagged sweep left the nonce %q on the row. That is a v0.0.7 "+
			"ledger replaying into a board its own daemon never built, which is the "+
			"one failure mode the flag exists to prevent", n)
	}
}

// And a sweep this build writes leaves it there.
func TestASweepFromThisBuildLeavesTheNonceOnTheRow(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)

	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "worker", NewToken: "tok-1", Nonce: "n-keepme",
	}, now); err != nil {
		t.Fatalf("register: %v", err)
	}
	id := s.Nonces["n-keepme"]

	stale := now.Add(s.Limits.AgentTTL + time.Minute)
	mustApply(t, s, &Op{Kind: OpSweep, StaleAgents: []string{id}, KeepArchivedNonce: true}, stale)
	mustApply(t, s, &Op{
		Kind: OpSweep, KeepArchivedNonce: true,
	}, stale.Add(s.Limits.StaleGrace+time.Minute))

	l := s.Agents[id]
	if l.Status != StatusArchived {
		t.Fatalf("setup did not reach the state under test: status %q", l.Status)
	}
	// The token still goes. It belongs to one activation, and its absence is
	// what makes the next call say E_BAD_TOKEN and name the way back.
	if l.Token != "" {
		t.Error("archiving left the activation's token in place: the agent would go " +
			"on making calls under a credential the board has stopped tracking")
	}
	if l.Nonce != "n-keepme" {
		t.Errorf("archiving cleared the nonce field to %q while s.Nonces kept the "+
			"index entry: the credential alive in one place and dead in the other is "+
			"what the engine's guard against recovering a privileged row without its "+
			"nonce trips on", l.Nonce)
	}
}

// AN ARCHIVED AGENT RESUMES, and it used to be told to register a new one.
//
// That advice was worse than the refusal it came with. Registering again under
// the same name while the board still holds the row forks a SIBLING: a second
// agent with an empty mailbox, beside an original whose mail nobody reads. The
// nonce index resolved the credential to a live row the whole time, and the row
// is kept for ArchiveRetention for exactly this.
func TestAnArchivedAgentCanResumeWithItsNonce(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)

	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "worker", NewToken: "tok-1", Nonce: "n-keepme",
		AgentKind: KindPersistent,
	}, now); err != nil {
		t.Fatalf("register: %v", err)
	}
	id := s.Nonces["n-keepme"]

	dormant := now.Add(s.Limits.AgentTTL + time.Minute)
	mustApply(t, s, &Op{Kind: OpSweep, StaleAgents: []string{id}, KeepArchivedNonce: true}, dormant)
	archived := dormant.Add(s.Limits.DormancyMax + time.Hour)
	mustApply(t, s, &Op{Kind: OpSweep, KeepArchivedNonce: true}, archived)
	if got := s.Agents[id].Status; got != StatusArchived {
		t.Fatalf("setup did not reach the state under test: status %q", got)
	}

	res, _, err := s.Apply(&Op{
		Kind: OpResume, Nonce: "n-keepme", ResumeID: "r-1", NewToken: "tok-2",
	}, archived.Add(time.Minute))
	if err != nil {
		t.Fatalf("resume of an archived agent was refused: %v\n"+
			"the nonce resolved to a row the board is keeping so it can come back, "+
			"and the only alternative offered was the one that forks a sibling", err)
	}
	if res["agent_id"] != id {
		t.Errorf("resume returned a different agent: %v", res["agent_id"])
	}
	l := s.Agents[id]
	if l.Status != StatusActive {
		t.Errorf("the resumed agent is %q, not active", l.Status)
	}
	if !l.ArchivedAt.IsZero() {
		t.Error("the resumed agent still carries the archival timestamp retention " +
			"counts from: a row that is demonstrably active saying it was retired")
	}
	if l.Nonce != "n-keepme" {
		t.Errorf("the resumed row has nonce %q: an active agent whose credential is "+
			"live in the index and missing from the row is what the privileged "+
			"recovery guard refuses", l.Nonce)
	}
}
