package core

import (
	"testing"
	"time"
)

// A subagent is its parent's work, not a third party to it.
//
// The ordinary delegation pattern is: claim the area, spawn a subagent to edit
// it. Without lineage the guard DENIED that subagent on its own parent's claim,
// telling it to "coordinate with agent parent" and "pick different work". The
// guard is an enforcement path rather than advice, so the harness then refused
// the edit outright: the exclusive claim locked out the very work it was taken
// for.
func TestAnAgentIsNotBlockedByItsOwnParentsClaim(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)
	reg := func(name, parent string) *Agent {
		t.Helper()
		op := &Op{Kind: OpRegister, Name: name, NewToken: "tok-" + name, Parent: parent}
		if parent != "" {
			// The parent vouches. Without this the lineage is a bare claim and
			// grants nothing, which is the whole point: an agent that merely
			// declared parent:"victim" used to be exempt from the victim's
			// exclusive claims here.
			nonce := "nonce-" + name + "-0123456789abcdef"
			if _, _, err := s.Apply(&Op{
				Kind: OpVouchChild, Token: s.Agents[parent].Token, Nonce: nonce,
			}, now); err != nil {
				t.Fatal(err)
			}
			op.ParentNonce = nonce
		}
		r, _, err := s.Apply(op, now)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := r["agent_id"].(string)
		l := s.Agents[id]
		if _, _, err := s.Apply(&Op{Kind: OpAckBoard, Token: l.Token}, now); err != nil {
			t.Fatal(err)
		}
		return l
	}
	parent := reg("parent", "")
	reg("sub", "parent")
	reg("grand", "sub") // delegation nests
	reg("stranger", "")
	if _, _, err := s.Apply(&Op{
		Kind: OpClaim, Token: parent.Token, Path: "/repo/internal/auth", Mode: ClaimExclusive,
	}, now); err != nil {
		t.Fatal(err)
	}

	file := "/repo/internal/auth/token.go"
	for _, who := range []string{"parent", "sub", "grand"} {
		if v := s.GuardPath(who, file, now); v.Decision != GuardAllow {
			t.Errorf("%s is the claim holder's own work and must not be blocked by it: %s. %s",
				who, v.Decision, v.Reason)
		}
	}
	// The point of the claim survives: an unrelated agent is still stopped.
	if v := s.GuardPath("stranger", file, now); v.Decision != GuardDeny {
		t.Fatalf("an exclusive claim must still hold against a third party, got %s", v.Decision)
	}
}

// Only that direction. A parent editing inside its SUBAGENT's claim is still
// stopped: the child asked not to be disturbed there, and a parent that means
// to overrule it has force_release.
func TestAParentIsStillStoppedByItsSubagentsClaim(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)
	reg := func(name, parent string) *Agent {
		t.Helper()
		op := &Op{Kind: OpRegister, Name: name, NewToken: "tok-" + name, Parent: parent}
		if parent != "" {
			// The parent vouches. Without this the lineage is a bare claim and
			// grants nothing, which is the whole point: an agent that merely
			// declared parent:"victim" used to be exempt from the victim's
			// exclusive claims here.
			nonce := "nonce-" + name + "-0123456789abcdef"
			if _, _, err := s.Apply(&Op{
				Kind: OpVouchChild, Token: s.Agents[parent].Token, Nonce: nonce,
			}, now); err != nil {
				t.Fatal(err)
			}
			op.ParentNonce = nonce
		}
		r, _, err := s.Apply(op, now)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := r["agent_id"].(string)
		l := s.Agents[id]
		if _, _, err := s.Apply(&Op{Kind: OpAckBoard, Token: l.Token}, now); err != nil {
			t.Fatal(err)
		}
		return l
	}
	reg("parent", "")
	sub := reg("sub", "parent")
	if _, _, err := s.Apply(&Op{
		Kind: OpClaim, Token: sub.Token, Path: "/repo/internal/auth", Mode: ClaimExclusive,
	}, now); err != nil {
		t.Fatal(err)
	}
	if v := s.GuardPath("parent", "/repo/internal/auth/token.go", now); v.Decision != GuardDeny {
		t.Fatalf("the child asked not to be disturbed; got %s", v.Decision)
	}
}

// A self-reported parent chain must never hang the writer loop.
func TestALoopedParentChainTerminates(t *testing.T) {
	s := NewState("t", DefaultLimits())
	// Proven links, so the walk actually traverses them: an unproven parent
	// stops the walk immediately and would make this test pass for the wrong
	// reason.
	s.Agents["a"] = &Agent{ID: "a", Parent: "b", ParentProven: true, Status: StatusActive}
	s.Agents["b"] = &Agent{ID: "b", Parent: "a", ParentProven: true, Status: StatusActive}
	if s.DescendsFrom("a", "nobody") {
		t.Fatal("a cycle must answer no, not yes")
	}
	if !s.DescendsFrom("a", "b") {
		t.Fatal("a real ancestor inside a cycle is still an ancestor")
	}
}

// A session id that was SUPPLIED and matched nothing is positive evidence this
// is a DIFFERENT session, not a hint to go looking for a neighbour.
//
// The directory fallback attributed any unregistered session to whichever
// single registered agent shared its working directory, which is the normal
// state of two agents in one repository. Verified against a running daemon
// before the fix: an unknown session id was handed the other agent's private
// mail INCLUDING the body, and guard_path answered decision=allow
// basis=no-claim for a path that agent held EXCLUSIVELY: the guard having
// resolved the stranger TO the claim holder and then reported that nothing
// claimed it.
//
// Raised by an independent reviewer (GPT-5.6-sol).
func TestAnUnknownSessionIsNotAttributedToItsNeighbour(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)
	res, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "registered", NewToken: "t1", SessionID: "sess-A",
		Agent: &AgentInfo{CWD: "/repo"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res["agent_id"].(string)

	// The owner is found by its own session id.
	if l := s.AgentForHook("sess-A", "/repo"); l == nil || l.ID != id {
		t.Fatal("the real owner must still be resolved by its session id")
	}
	// A hook that genuinely does not know its session id is still matched by
	// directory: that is what the fallback is FOR, and it stays.
	if l := s.AgentForHook("", "/repo"); l == nil || l.ID != id {
		t.Fatal("a hook sending no session id must still be matched by directory")
	}
	// But a session that named itself and matched nothing is somebody else.
	if l := s.AgentForHook("some-other-session", "/repo"); l != nil {
		t.Fatalf("an unknown session must not inherit a neighbour's identity, got %q", l.ID)
	}
}

// The consequence that made it serious: the guard resolved the stranger to the
// claim holder, so a path held EXCLUSIVELY was reported as unclaimed.
func TestAStrangerIsNotHandedTheClaimHoldersIdentity(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)
	res, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "holder", NewToken: "t1", SessionID: "sess-A",
		Agent: &AgentInfo{CWD: "/repo"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res["agent_id"].(string)
	if _, _, err := s.Apply(&Op{Kind: OpAckBoard, Token: s.Agents[id].Token}, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Apply(&Op{
		Kind: OpClaim, Token: s.Agents[id].Token, Path: "/repo", Mode: ClaimExclusive,
	}, now); err != nil {
		t.Fatal(err)
	}

	// Resolved as the holder itself, the guard would allow and say "no claim".
	// Resolved as nobody, it still allows (failing open is deliberate) but
	// the reason is honest, and the caller can tell the two apart.
	stranger := s.AgentForHook("some-other-session", "/repo")
	if stranger != nil {
		t.Fatalf("precondition: the stranger must not resolve, got %q", stranger.ID)
	}
	if v := s.GuardPath("", "/repo/x.go", now); v.Decision != GuardAllow {
		t.Fatalf("an unidentified caller is allowed by design, got %s", v.Decision)
	}
	// And the holder's claim genuinely does stop a DIFFERENT registered agent.
	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "other", NewToken: "t2", SessionID: "sess-B",
	}, now); err != nil {
		t.Fatal(err)
	}
	if v := s.GuardPath("other", "/repo/x.go", now); v.Decision != GuardDeny {
		t.Fatalf("the exclusive claim must still hold against a third party, got %s", v.Decision)
	}
}

// `parent` arrives as a bare string on the wire, and the powers keyed off it
// are not cosmetic: a subagent speaks under its parent's membership, skips an
// exclusive space's queue, and is exempt from its parent's exclusive claims in
// the guard.
//
// Verified against a running daemon before this: an agent registering with
// parent:"victim" posted into the victim's exclusive space, joined it instead of
// queueing, and got allow/no-claim for a path the victim held exclusively.
//
// Two of those powers were added earlier in this same session, to fix a real
// subagent deadlock and a real guard block: without ever asking how we know it
// IS a subagent. Raised by an independent reviewer (GPT-5.6-sol).
func TestLineageGrantsNothingUntilTheParentVouchesForIt(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)
	mk := func(name, tok string) *Agent {
		t.Helper()
		r, _, err := s.Apply(&Op{Kind: OpRegister, Name: name, NewToken: tok}, now)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := r["agent_id"].(string)
		if _, _, err := s.Apply(&Op{Kind: OpAckBoard, Token: s.Agents[id].Token}, now); err != nil {
			t.Fatal(err)
		}
		return s.Agents[id]
	}
	victim := mk("victim", "t1")
	if _, _, err := s.Apply(&Op{
		Kind: OpClaim, Token: victim.Token, Path: "/repo", Mode: ClaimExclusive,
	}, now); err != nil {
		t.Fatal(err)
	}

	// An impostor simply says so. Recorded as a claim, granting nothing.
	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "impostor", NewToken: "t2", Parent: "victim",
	}, now); err != nil {
		t.Fatal(err)
	}
	if s.Agents["impostor"].ParentProven {
		t.Fatal("nobody vouched for this lineage")
	}
	if s.DescendsFrom("impostor", "victim") {
		t.Error("an unvouched lineage must not be walked: this decides whether the guard " +
			"waives somebody's exclusive claim")
	}
	if v := s.GuardPath("impostor", "/repo/x.go", now); v.Decision != GuardDeny {
		t.Errorf("the victim's exclusive claim must hold against an impostor, got %s", v.Decision)
	}

	// The real thing: only the parent can vouch, because only the parent holds
	// the token vouch_child requires.
	const nonce = "child-nonce-0123456789abcdef"
	if _, _, err := s.Apply(&Op{Kind: OpVouchChild, Token: victim.Token, Nonce: nonce}, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "realchild", NewToken: "t3",
		Parent: "victim", ParentNonce: nonce,
	}, now); err != nil {
		t.Fatal(err)
	}
	if !s.Agents["realchild"].ParentProven {
		t.Fatal("a vouched child must be proven")
	}
	if v := s.GuardPath("realchild", "/repo/x.go", now); v.Decision != GuardAllow {
		t.Errorf("a genuine subagent is its parent's own work, got %s", v.Decision)
	}

	// One-time. A proof that can be replayed is a standing capability, and this
	// one grants another agent's guard exemption.
	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "replay", NewToken: "t4",
		Parent: "victim", ParentNonce: nonce,
	}, now); err != nil {
		t.Fatal(err)
	}
	if s.Agents["replay"].ParentProven {
		t.Error("the voucher is consumed on first use; a replay proves nothing")
	}
	if v := s.GuardPath("replay", "/repo/x.go", now); v.Decision != GuardDeny {
		t.Errorf("and a replayed nonce must not buy the exemption, got %s", v.Decision)
	}
}
