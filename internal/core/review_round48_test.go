package core

import (
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
