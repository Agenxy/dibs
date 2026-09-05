package core

import (
	"testing"
	"time"
)

// A ledger written before the purge fix still replays.
//
// The fix changed what an EXISTING op does. Replay runs today's Apply over
// yesterday's ops, so a sweep recorded by v0.0.6, which purged the agent row
// and left its mail, was replayed with the new behaviour and deleted that mail
// at the historical point: a later acknowledgement which v0.0.6 had accepted
// and ledgered then referred to a message that no longer existed, returned
// E_NO_MESSAGE, and the daemon refused its own ledger. An upgrade that will not
// start is worse than the inheritance it was closing.
//
// The sweep here carries no PurgeMail flag, which is exactly what every
// historical sweep looks like. AGENTS.md gives this rule for a new RULE in
// Apply; it is the same hazard for changed BEHAVIOUR, and the same answer:
// record the decision in the Op.
func TestALedgerWrittenBeforeThePurgeFixStillReplays(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Now()
	peer := reg(t, s, "peer", "tok-peer", now)
	regPersistent(t, s, "alice", "tok-alice", "n-alice", now)

	res := mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: peer.Token, To: "alice",
		MsgType: MsgNotify, Body: "for alice",
	}, now)
	serial := res["msg_serial"].(uint64)

	s.Agents["alice"].Status = StatusArchived
	s.Agents["alice"].ArchivedAt = now.Add(-s.Limits.ArchiveRetention - time.Hour)

	// The historical sweep. Under v0.0.6 this purged the row and LEFT the mail.
	mustApply(t, s, &Op{Kind: OpSweep}, now)

	// A replacement takes the name, inherits the id, and acknowledges the
	// inherited message: both ledgered, both valid under v0.0.6.
	regPersistent(t, s, "alice", "tok-alice-2", "n-alice-2", now)
	if _, _, err := s.Apply(&Op{
		Kind: OpAckMessage, Token: "tok-alice-2", MsgSerial: serial,
	}, now); err != nil {
		t.Fatalf("REPLAY WOULD FAIL HERE: %v\n\nA v0.0.6 ledger holds this ack as a "+
			"valid record. Replaying it through the current fold deletes the message "+
			"at the historical sweep, so the ack refers to nothing and the daemon "+
			"refuses its own ledger on upgrade", err)
	}
}
