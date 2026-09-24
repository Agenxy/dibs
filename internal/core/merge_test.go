package core

import (
	"testing"
	"time"
)

func seat(t *testing.T, s *State, name, nonce string, now time.Time) *Agent {
	t.Helper()
	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: name, NewToken: "tok-" + name, Nonce: nonce,
		AgentKind: KindPersistent, Agent: &AgentInfo{CWD: "/repo"},
	}, now); err != nil {
		t.Fatal("setup:", err)
	}
	// The awareness gate wants the board acknowledged before an agent may act,
	// which check_in does in life and this stands in for: these tests are
	// about the merge, not about the gate.
	s.Agents[name].AckedSerial = s.Serial
	return s.Agents[name]
}

// A FORKED SEAT GETS ITS MAIL BACK.
//
// A persistent agent's nonce is the credential that survives its process, and
// an agent cannot carry a secret across a context boundary: the context ends,
// which is the event the nonce exists for, and the nonce goes with it. The
// next session registers under the same name, becomes a sibling, and cannot
// read a word of its predecessor's mail. The bridge keeping the nonce is the
// fix; this is the repair for the boards that already have the scars. One had
// nine rows for five roles.
func TestAMergedForkHandsOverItsMailAndClaims(t *testing.T) {
	now := time.Now()
	s := NewState("n1", DefaultLimits())
	fork := seat(t, s, "seat-2", "n-fork", now)
	keep := seat(t, s, "seat", "n-keep", now)
	sender := seat(t, s, "peer", "n-peer", now)

	if _, _, err := s.Apply(&Op{
		Kind: OpSendMessage, Token: sender.Token, To: fork.ID,
		MsgType: MsgQuestion, Body: "did the fork get this",
	}, now); err != nil {
		t.Fatal("setup:", err)
	}
	if _, _, err := s.Apply(&Op{
		Kind: OpClaim, Token: fork.Token, Mode: ClaimExclusive, Dirs: []string{"/repo/pkg"},
	}, now); err != nil {
		t.Fatal("setup:", err)
	}
	// SETUP ASSERTED: the mail really is the fork's, or the merge below has
	// nothing to move and would pass for the wrong reason.
	if got := len(s.Inbox(fork.ID)); got != 1 {
		t.Fatalf("setup: the fork has %d messages, want 1", got)
	}
	if len(s.Inbox(keep.ID)) != 0 {
		t.Fatal("setup: the survivor already has mail")
	}
	fork.Status = StatusDormant // a merge refuses a live agent; see below

	r, evs, err := s.Apply(&Op{Kind: OpMergeAgents, To: fork.ID, MergeInto: keep.ID}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(s.Inbox(keep.ID)); got != 1 {
		t.Errorf("the survivor has %d messages, want the fork's 1: a mailbox "+
			"nobody can open is the whole injury", got)
	}
	if got := len(s.Inbox(fork.ID)); got != 0 {
		t.Errorf("the fork still holds %d message(s)", got)
	}
	// BY OWNER, NOT BY PATH. The first version of this compared the path to
	// what the test passed in and failed while the merge was working: a claim
	// is stored relative to the agent's own directory, so "/repo/pkg" from an
	// agent in /repo is recorded as ".". What this test is about is who holds
	// it, and asserting the spelling instead would have sent me to fix code
	// that was correct.
	var held bool
	for _, c := range s.Claims {
		if c.Agent == keep.ID {
			held = true
		}
		if c.Agent == fork.ID {
			t.Error("a claim is still held by the absorbed row, which nobody is behind")
		}
	}
	if !held {
		t.Error("the claim did not move, so the path is held by nobody and " +
			"released by nothing")
	}
	// THE NONCE FOLLOWS, or the next session reopens the fork this just closed.
	if s.Nonces["n-fork"] != keep.ID {
		t.Errorf("the fork's nonce still points at %q, so the repair undoes "+
			"itself on the next restart", s.Nonces["n-fork"])
	}
	if s.Agents[fork.ID].MergedInto != keep.ID {
		t.Error("the closed row does not say where its mail went, so a reader " +
			"cannot tell a merge from data loss")
	}
	if s.Agents[fork.ID].Status != StatusClosed || s.Agents[fork.ID].Token != "" {
		t.Error("the absorbed row is still usable")
	}
	if len(evs) == 0 || evs[0].Type != "agent.merged" {
		t.Errorf("no agent.merged event, so nothing is ledgered and a restart "+
			"undoes the repair: %v", evs)
	}
	if r["messages"] != 1 {
		t.Errorf("the result says %v messages moved", r["messages"])
	}
}

// A LIVE AGENT IS NOT A DUPLICATE OF ANYTHING.
func TestMergingALiveAgentIsRefused(t *testing.T) {
	now := time.Now()
	s := NewState("n1", DefaultLimits())
	fork := seat(t, s, "seat-2", "n-fork", now)
	keep := seat(t, s, "seat", "n-keep", now)
	if fork.Status != StatusActive {
		t.Fatalf("setup: the fork is %q, so this does not test a live merge", fork.Status)
	}
	_, _, err := s.Apply(&Op{Kind: OpMergeAgents, To: fork.ID, MergeInto: keep.ID}, now)
	if err == nil {
		t.Fatal("a running agent was merged away while it was working")
	}
	if len(s.Inbox(keep.ID)) != 0 || s.Agents[fork.ID].Status == StatusClosed {
		t.Error("and it was partly applied before refusing")
	}
}

// A PATH BOTH ROWS HOLD IS A CONFLICT AN ADMIN HAS TO SEE, because folding it
// silently drops a claim, and a claim that vanishes is how two agents end up
// writing the same file.
func TestAnOverlappingClaimRefusesTheMerge(t *testing.T) {
	now := time.Now()
	s := NewState("n1", DefaultLimits())
	fork := seat(t, s, "seat-2", "n-fork", now)
	keep := seat(t, s, "seat", "n-keep", now)
	for _, a := range []*Agent{fork, keep} {
		if _, _, err := s.Apply(&Op{Kind: OpClaim, Token: a.Token, Mode: ClaimShared, Dirs: []string{"/repo/same"}}, now); err != nil {
			t.Skipf("this board will not let two agents claim one path: %v", err)
		}
	}
	fork.Status = StatusDormant
	_, _, err := s.Apply(&Op{Kind: OpMergeAgents, To: fork.ID, MergeInto: keep.ID}, now)
	if err == nil {
		t.Fatal("the merge silently dropped one of two claims on the same path")
	}
}

// The shape checks are Admit's, so a caller gets them before any state is read.
func TestMergeRefusesNonsenseAtIngress(t *testing.T) {
	lim := DefaultLimits()
	if err := Admit(&Op{Kind: OpMergeAgents, To: "a"}, lim); err == nil {
		t.Error("a merge naming only one agent was admitted")
	}
	if err := Admit(&Op{Kind: OpMergeAgents, To: "a", MergeInto: "a"}, lim); err == nil {
		t.Error("an agent was admitted to be merged into itself")
	}
	if err := Admit(&Op{Kind: OpMergeAgents, To: "a", MergeInto: "b"}, lim); err != nil {
		t.Errorf("a well-formed merge was refused at ingress: %v", err)
	}
}
