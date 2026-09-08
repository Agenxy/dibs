package core

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A persistent agent moved from thread A to B; recovering it by name and
// its retained session id A, with no pid stated, put the current session
// back to A and kept B's process: when B exited the sweep retired the
// recovered agent. The reattach path applies the activation rule the other
// two recovery paths apply.
func TestAReattachBySessionIDDropsTheOtherThreadsProcess(t *testing.T) {
	const (
		threadA = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
		threadB = "01a00042-2222-7f60-81cc-6ab1298d76ec"
	)
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)
	// No nonce chosen: the minted one leaves the row reclaimable by name
	// and session id, which is the path under test.
	mustApply(t, s, &Op{Kind: OpRegister, Name: "r", NewToken: "tok-1", MintedNonce: "n-minted-0123456789", AgentKind: KindPersistent, SessionID: threadA, PID: 1, ProcStart: 5, V7Semantics: true}, now)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "tok-1", SessionAlias: threadB, V7Semantics: true}, now.Add(time.Minute))
	if s.Agents["r"].CurrentSession != threadB {
		t.Fatal("setup: the hook did not move the row to thread B")
	}
	s.Agents["r"].PID, s.Agents["r"].ProcStart = 2, 6 // B's process
	res := mustApply(t, s, &Op{Kind: OpRegister, Name: "r", NewToken: "tok-2", MintedNonce: "n-minted-2-0123456789", AgentKind: KindPersistent, SessionID: threadA, V7Semantics: true}, now.Add(2*time.Minute))
	if res["reattached"] != true && res["resumed"] != true {
		t.Fatalf("setup: the register did not reattach by session id: %v", res)
	}
	l := s.Agents["r"]
	if l.CurrentSession != threadA {
		t.Fatalf("setup: after reattaching by A the current session is %q", l.CurrentSession)
	}
	if l.PID != 0 {
		t.Fatalf("reattached by session A with no pid stated, the row keeps pid %d: thread B's process, "+
			"and its exit retires the recovered agent", l.PID)
	}
	// Reattaching by the CURRENT session is the same activation and keeps
	// what it knew.
	s.Agents["r"].PID = 3
	mustApply(t, s, &Op{Kind: OpRegister, Name: "r", NewToken: "tok-3", MintedNonce: "n-minted-3-0123456789", AgentKind: KindPersistent, SessionID: threadA, V7Semantics: true}, now.Add(3*time.Minute))
	if got := s.Agents["r"].PID; got != 3 {
		t.Fatalf("reattaching by the current session dropped pid 3 to %d", got)
	}
}

// Every persistent registration is handed a nonce, and one Dibs minted does
// not close the guessable path: the row stays reclaimable by name and
// session id, deliberately. The warning that says so required an EMPTY
// nonce, so an agent that let Dibs mint one was told nothing.
func TestAMintedNonceRegistrationIsToldItStaysReclaimable(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)
	res := mustApply(t, s, &Op{Kind: OpRegister, Name: "m", NewToken: "tok-m", MintedNonce: "n-minted-0123456789", AgentKind: KindPersistent, SessionID: "host-77", V7Semantics: true}, now)
	rec, _ := res["recovery"].(string)
	if !strings.Contains(rec, "ALSO be reclaimed") {
		t.Fatalf("a registration with a minted nonce is not told it stays reclaimable by name and session id: %q", rec)
	}
	if strings.Contains(rec, "re-register") {
		t.Fatalf("the warning tells a minted-nonce agent to re-register, which forks a sibling: %q", rec)
	}
	// One that chose its nonce is told nothing of the kind.
	res = mustApply(t, s, &Op{Kind: OpRegister, Name: "c", NewToken: "tok-c", Nonce: "n-chosen-0123456789", AgentKind: KindPersistent, SessionID: "host-78", V7Semantics: true}, now)
	if rec, _ := res["recovery"].(string); rec != "" {
		t.Fatalf("an agent that chose its nonce is warned about recovery: %q", rec)
	}
}

// The activation rule on the reattach path is gated on the recorded
// semantics, like the other two paths: a v0.0.6 reattach with a new thread
// alias and no pid kept the recorded process, and replaying it under the
// new rule rebuilt a different one.
func TestAHistoricalReattachKeepsItsRecordedProcess(t *testing.T) {
	const (
		threadA = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
		threadB = "01a00042-2222-7f60-81cc-6ab1298d76ec"
	)
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)
	// A row as v0.0.6 wrote it: no semantics flag on any op.
	mustApply(t, s, &Op{Kind: OpRegister, Name: "r", NewToken: "tok-1", AgentKind: KindEphemeral, SessionID: threadA, PID: 1, ProcStart: 5}, now)
	res := mustApply(t, s, &Op{Kind: OpRegister, Name: "r", NewToken: "tok-2", AgentKind: KindEphemeral, SessionID: threadA, SessionAlias: threadB}, now.Add(time.Minute))
	if res["reattached"] != true && res["resumed"] != true {
		t.Fatalf("setup: the historical register did not reattach: %v", res)
	}
	if got := s.Agents["r"].PID; got != 1 {
		t.Fatalf("a v0.0.6 reattach replayed under today's fold rebuilt pid %d, want the recorded 1: "+
			"the daemon's own history folds differently after the upgrade", got)
	}
}

// The fold dropped the caller's own binding on the caller's TOKEN, which is
// not ledgered: live, both bindings moved to the sibling; on replay the
// token was gone and the active row kept its alias, two active holders of
// one thread after a restart, and a state that was not the fold of its
// ledger. The ingress records the alias's holder in a field of its own, and
// the fold drops on that. Every op below is round-tripped through JSON, as
// the ledger does, before it is applied.
func TestASiblingsTwoTakesReplayFromTheLedger(t *testing.T) {
	const (
		aliasT   = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
		primaryS = "01a00042-2222-7f60-81cc-6ab1298d76ec"
	)
	now := time.Unix(1700000000, 0)
	ops := []*Op{
		{Kind: OpRegister, Name: "a", NewToken: "tok-a", Nonce: "n-a-0123456789abcdef", AgentKind: KindPersistent, SessionAlias: aliasT, V7Semantics: true},
		{Kind: OpRegister, Name: "b", NewToken: "tok-b", Nonce: "n-b-0123456789abcdef", AgentKind: KindPersistent, SessionID: primaryS, V7Semantics: true},
		// The sibling, as the ingress writes it: minted with a's token, taking
		// b's primary and a's alias, each holder recorded.
		{
			Kind: OpRegister, Name: "sibling", Token: "tok-a", NewToken: "tok-s", Nonce: "n-s-0123456789abcdef", AgentKind: KindPersistent,
			SessionID: primaryS, SessionAlias: aliasT, SessionTakenFrom: "b", SessionAliasTakenFrom: "a", V7Semantics: true,
		},
	}
	replay := NewState("t", DefaultLimits())
	for i, op := range ops {
		blob, err := json.Marshal(op)
		if err != nil {
			t.Fatal(err)
		}
		var ledgered Op
		if err := json.Unmarshal(blob, &ledgered); err != nil {
			t.Fatal(err)
		}
		if ledgered.Token != "" {
			t.Fatal("setup: the token survived the ledger, so this replays nothing")
		}
		if i == 1 {
			// b goes dormant before the sibling registers.
			mustApply(t, replay, &ledgered, now)
			replay.Agents["b"].Status = StatusDormant
			continue
		}
		mustApply(t, replay, &ledgered, now)
	}
	if !replay.Agents["sibling"].holdsSession(aliasT) || !replay.Agents["sibling"].holdsSession(primaryS) {
		t.Fatal("setup: the replayed sibling did not take both bindings")
	}
	if replay.Agents["a"].holdsSession(aliasT) {
		t.Fatal("replayed from the ledger, the active row still holds the alias the sibling took: the " +
			"live fold dropped it on a token the ledger does not carry")
	}
	if replay.Agents["b"].holdsSession(primaryS) {
		t.Fatal("replayed from the ledger, the dormant row still holds the primary the sibling took")
	}
}
