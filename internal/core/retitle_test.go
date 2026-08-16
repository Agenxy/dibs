package core

import "testing"

// A member can redact a space topic without destroying the space.
//
// There was no way to change a topic. An agent in a private repository declared
// richly, as dibs://skills tells it to, and the wording became a durable board
// object visible to agents in unrelated repositories. The only remedy it could
// find was destroying the space, which also destroys the coordination the space
// exists for, and its operator had to be the one to notice.
func TestAMemberCanRedactASpaceTopic(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	a := reg(t, s, "worker", "tok-worker", t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: a.Token}, t0)
	mustApply(t, s, &Op{
		Kind: OpSpaceOpen, Token: a.Token, Space: "issue-42",
		Text: "fixing auth on forge-prod-07 with svc-ci-deploy",
	}, t0)

	res := mustApply(t, s, &Op{
		Kind: OpSpaceRetitle, Token: a.Token, Space: "issue-42",
		Text: "auth work (details withheld)",
	}, t0)

	if res["topic"] != "auth work (details withheld)" {
		t.Errorf("topic = %v, want the redacted text", res["topic"])
	}
	if got := s.Spaces["issue-42"].Topic; got != "auth work (details withheld)" {
		t.Errorf("stored topic = %q: the leak is still on the board", got)
	}
	// And the space itself survives, which is the whole point of redacting
	// rather than closing.
	if s.Spaces["issue-42"] == nil {
		t.Error("the space was destroyed by redacting it")
	}
}

// The redaction must be LEDGERED, or the old topic returns on restart.
//
// This is the third time this class has bitten (prune, claim_coordinator,
// prune_own): an unledgered mutation looks right in memory and is undone by
// replay. Here it would resurrect the exact text somebody asked to remove.
func TestRedactingATopicAdvancesTheSerial(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	a := reg(t, s, "worker", "tok-worker", t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: a.Token}, t0)
	mustApply(t, s, &Op{Kind: OpSpaceOpen, Token: a.Token, Space: "issue-42", Text: "secret"}, t0)
	before := s.Serial

	mustApply(t, s, &Op{Kind: OpSpaceRetitle, Token: a.Token, Space: "issue-42", Text: "redacted"}, t0)

	if s.Serial == before {
		t.Fatal("the serial did not move, so the redaction is not ledgered and the " +
			"original topic comes back on the next restart")
	}
}

// A stranger cannot retitle: the label is the space's own, and membership is
// the boundary that already governs reading it.
func TestANonMemberCannotRetitle(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	a := reg(t, s, "owner", "tok-owner", t0)
	b := reg(t, s, "stranger", "tok-stranger", t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: a.Token}, t0)
	mustApply(t, s, &Op{Kind: OpSpaceOpen, Token: a.Token, Space: "issue-42", Text: "mine"}, t0)

	if _, _, err := s.Apply(&Op{
		Kind: OpSpaceRetitle, Token: b.Token, Space: "issue-42", Text: "hijacked",
	}, t0); err == nil {
		t.Fatal("a non-member retitled somebody else's space")
	}
	if got := s.Spaces["issue-42"].Topic; got != "mine" {
		t.Errorf("topic changed despite the refusal: %q", got)
	}
}

// The event must not republish what was just redacted.
func TestTheRedactionEventDoesNotCarryTheOldTopic(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	a := reg(t, s, "worker", "tok-worker", t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: a.Token}, t0)
	secret := "forge-prod-07 svc-ci-deploy /opt/k7/secrets"
	mustApply(t, s, &Op{Kind: OpSpaceOpen, Token: a.Token, Space: "issue-42", Text: secret}, t0)

	_, evs, err := s.Apply(&Op{
		Kind: OpSpaceRetitle, Token: a.Token, Space: "issue-42", Text: "redacted",
	}, t0)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	for _, ev := range evs {
		for _, v := range ev.Data {
			if str, ok := v.(string); ok && str == secret {
				t.Error("the redaction event carries the old topic, so removing it from the " +
					"board publishes it to the activity feed and the ledger instead")
			}
		}
	}
}
