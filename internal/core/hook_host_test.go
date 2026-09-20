package core

import (
	"testing"
	"time"
)

// The host-scoped session lookup keeps every preference the plain one has.
//
// AgentForHookOn asked AgentBySession first and, when that answer was on
// another machine, took the FIRST holder Go's map iteration reached on the
// caller's host. Every agent registering through one bridge states the same
// `host-<ppid>`, so on a machine with two of them a hook or guard from that
// machine resolved to either, per call: a guard attributed to the claim
// holder allowed the other sibling's edit. The stated-over-guessed,
// active-over-idle and held-first preferences exist for exactly this and
// were discarded the moment a host entered the question. Round fifteen of
// the pre-release review.
func TestAHostScopedSessionLookupPrefersTheFirstHolder(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)
	const shared = "host-12345"
	register := func(name, token, host string) *Agent {
		t.Helper()
		res, _, err := s.Apply(&Op{
			Kind: OpRegister, Name: name, NewToken: token, SessionID: shared,
			Agent: &AgentInfo{CWD: "/w/repo", HostID: host},
		}, now)
		if err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
		if _, _, err := s.Apply(&Op{Kind: OpAckBoard, Token: token}, now); err != nil {
			t.Fatalf("ack %s: %v", name, err)
		}
		return s.Agents[res["agent_id"].(string)]
	}
	register("alpha", "tok-a", "machine-a")
	beta := register("beta", "tok-b", "machine-b")
	register("gamma", "tok-c", "machine-b")

	// Map order is random per iteration; one wrong answer is the bug.
	for i := range 200 {
		got := s.AgentForHookOn(shared, "/w/repo", "machine-b")
		if got == nil || got.ID != beta.ID {
			t.Fatalf("call %d: the host-scoped lookup resolved to %v, want the first holder "+
				"%s", i, got, beta.ID)
		}
	}
}

// Two agents on two machines with one synthetic session id are two pairs
// of hands.
//
// SameHands reads a shared session, or a registration provenance the other
// holds, as one agent under two names: that is what a coordinator's puppet
// looks like, minted from the coordinator's own bridge. Both `host-<ppid>`
// and the provenance copied from it repeat across computers, so two
// independent bridges on two machines that happened to share a pid read as
// one agent, and adopting a stranded mailbox onto the genuinely separate
// one was refused with E_NOT_PERMITTED and a hint (register through your
// own bridge) that could not change the answer. Session evidence speaks
// for one machine only; a proven parent still speaks across them. Round
// twenty-one of the pre-release review.
func TestSessionEvidenceOfOneAgentSpeaksForOneMachineOnly(t *testing.T) {
	on := func(host string) *Agent {
		return &Agent{
			ID: "agent-" + host, SessionID: "host-12345", RegisteredFrom: "host-12345",
			Agent: &AgentInfo{CWD: "/w/repo", HostID: host},
		}
	}
	if on("machine-a").SameHands(on("machine-b")) {
		t.Fatal("two bridges on two machines sharing a pid read as one agent: adoption onto " +
			"the genuinely separate one is refused, and its hint cannot change that")
	}
	if !on("machine-a").SameHands(on("machine-a")) {
		t.Fatal("two rows from one bridge on one machine no longer read as one agent: the " +
			"puppet route is open again")
	}
	// A row that recorded no machine keeps the old answer, and so does one
	// whose partner did not.
	noHost := on("")
	if !on("machine-a").SameHands(noHost) || !noHost.SameHands(on("machine-b")) {
		t.Fatal("a row with no recorded machine stopped matching the session it shares")
	}
	// Lineage is proven by a vouched secret, not by a session, and crosses
	// machines.
	child := on("machine-b")
	child.Parent, child.ParentProven = "agent-machine-a", true
	if !on("machine-a").SameHands(child) {
		t.Fatal("a proven child on another machine is no longer the parent's hands")
	}
}
