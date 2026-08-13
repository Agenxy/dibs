package core

import "testing"

// The agent that started the daemon can take the coordinator role.
//
// Roles were reachable only through the human admin path, so a fleet with
// nobody at the keyboard could never have a coordinator: force_release,
// close_space and clearing another agent's debris were permanently out of
// reach, on a tool whose whole claim is that agents drive it.
func TestTheLaunchingAgentCanClaimCoordinator(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	a := regPersistent(t, s, "boss", "tok-boss", "boss-nonce-0123456789abcdef", t0)

	res := mustApply(t, s, &Op{Kind: OpClaimCoordinator, Token: a.Token, ClaimVerified: true}, t0)
	if res["role"] != RoleCoordinator {
		t.Fatalf("role = %v, want %s", res["role"], RoleCoordinator)
	}
	if !s.Agents[a.ID].IsCoordinator() {
		t.Error("the agent does not hold the role it was just granted")
	}
}

// Without the secret there is no claim.
//
// ClaimVerified is the engine's recorded verdict, never the caller's assertion:
// the engine blanks it on ingress exactly as it blanks AgentID. This asserts the
// core half, that a false verdict is refused.
func TestAClaimWithoutTheSecretIsRefused(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	a := regPersistent(t, s, "boss", "tok-boss", "boss-nonce-0123456789abcdef", t0)

	if _, _, err := s.Apply(&Op{Kind: OpClaimCoordinator, Token: a.Token}, t0); err == nil {
		t.Fatal("an unverified claim was granted: any agent could take the role by asking")
	}
	if s.Agents[a.ID].IsCoordinator() {
		t.Error("the role was granted despite the refusal")
	}
}

// The role has to outlive the process, so only a persistent agent may hold it.
//
// An ephemeral claimant would carry the role into a closed record when it signs
// off, and the claim is single-use: the board would be left with no coordinator
// and no way to appoint one.
func TestAnEphemeralAgentCannotClaimCoordinator(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	a := reg(t, s, "passing-through", "tok-eph", t0)

	_, _, err := s.Apply(&Op{Kind: OpClaimCoordinator, Token: a.Token, ClaimVerified: true}, t0)
	if err == nil {
		t.Fatal("an ephemeral agent took the role; signing off would strand the board")
	}
	if s.Agents[a.ID].IsCoordinator() {
		t.Error("the role was granted despite the refusal")
	}
}

// The role survives a restart, which is the point of requiring a nonce.
//
// Resume reattaches the same record by nonce and never touches Role, so this
// pins behaviour that is currently free: a future resume that rebuilt the agent
// instead of reattaching it would silently demote every coordinator.
func TestCoordinatorSurvivesAResume(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	a := regPersistent(t, s, "boss", "tok-boss", "boss-nonce-0123456789abcdef", t0)
	mustApply(t, s, &Op{Kind: OpClaimCoordinator, Token: a.Token, ClaimVerified: true}, t0)

	mustApply(t, s, &Op{
		Kind: OpResume, Nonce: "boss-nonce-0123456789abcdef",
		ResumeID: "r1", NewToken: "tok-boss-2",
	}, t0)

	if !s.Agents[a.ID].IsCoordinator() {
		t.Error("the agent lost coordinator across a resume: the role must outlive the process")
	}
}

// The grant must be LEDGERED, or it does not outlive the process.
//
// Every test above passed while the role was granted in memory and never
// written down: the serial did not move, so the engine never appended, and a
// restart replayed a board where the claim had never happened. It was caught by
// restarting a real daemon, not by any of them. The serial is what the engine
// keys ledgering on, so that is what this asserts.
func TestClaimingCoordinatorAdvancesTheSerial(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	a := regPersistent(t, s, "boss", "tok-boss", "boss-nonce-0123456789abcdef", t0)
	before := s.Serial

	mustApply(t, s, &Op{Kind: OpClaimCoordinator, Token: a.Token, ClaimVerified: true}, t0)

	if s.Serial == before {
		t.Fatal("the serial did not move, so the engine will not ledger the grant and a " +
			"restart silently demotes the coordinator")
	}
}

// A board that already has a coordinator is not asked again.
func TestHasCoordinatorSeesTheRole(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	if s.HasCoordinator() {
		t.Fatal("an empty board reports a coordinator")
	}
	a := regPersistent(t, s, "boss", "tok-boss", "boss-nonce-0123456789abcdef", t0)
	mustApply(t, s, &Op{Kind: OpClaimCoordinator, Token: a.Token, ClaimVerified: true}, t0)
	if !s.HasCoordinator() {
		t.Error("HasCoordinator missed a live coordinator, so the daemon would mint a second claim")
	}
}
