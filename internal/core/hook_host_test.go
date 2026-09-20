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
