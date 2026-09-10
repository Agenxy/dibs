package main

import (
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
)

// REMOVING A ROLE FROM dibs.toml TAKES IT AWAY. Issue #73.
//
// `[roles]` decided what got GRANTED and never what got withdrawn. A role is
// replayable state, so an operator who deleted a name from the file watched
// the ledger restore the agent as admin on the next boot and the reconciler
// simply decline to grant it again: the god view over every mailbox, held by
// an agent the config no longer named, until somebody ran `dibs admin member`
// by hand. The two-step was documented, which is better than pretending, and
// still a config file that says one thing while the board does another.
//
// The rule, from the issue's own sketch: withdraw only what this mechanism
// granted AND can still prove it granted. The pin file records which
// credential each declared role went to. A pinned name the config no longer
// declares, held by the agent whose fingerprint is pinned, is demoted and the
// pin dropped. Anything else is left alone: a role a person granted by hand,
// or one an agent took through the launch claim, was never this mechanism's
// to withdraw, and the pin says so because its fingerprint does not match.
func TestARoleRemovedFromTheConfigIsWithdrawn(t *testing.T) {
	eng, ctx := testEngine(t)
	pins := loadRolePins(t.TempDir())

	registerAgentAs(t, eng, "release-manager", "nonce-rm")
	declared := RolesConfig{
		Admin:    []string{"release-manager"},
		Identity: map[string]string{"release-manager": engine.RolePinFingerprint("nonce-rm")},
	}
	applyDeclaredRoles(ctx, eng, declared, pins)
	if !holdsRole(t, eng, "release-manager", core.RoleAdmin) {
		t.Fatal("setup: the declared admin was not granted")
	}

	// The operator deletes the line. Next tick.
	applyDeclaredRoles(ctx, eng, RolesConfig{}, pins)
	if holdsRole(t, eng, "release-manager", core.RoleAdmin) {
		t.Fatal("release-manager still holds admin after being removed from [roles]: " +
			"the config grants standing privilege and cannot take it back, so the " +
			"file says one thing and the board does another")
	}
	if _, pinned := pins.Pins[core.RoleAdmin]["release-manager"]; pinned {
		t.Error("the pin outlived the role it recorded, so a later agent under that " +
			"name would be judged against a grant that no longer exists")
	}
}

// Pointing [roles.identity] at a SUCCESSOR withdraws the role from the agent
// that held it under the old fingerprint. The name is the same; the config
// now authorises a different credential, and the pinned one is not it.
func TestARoleRepinnedToASuccessorLeavesThePredecessor(t *testing.T) {
	eng, ctx := testEngine(t)
	pins := loadRolePins(t.TempDir())

	registerAgentAs(t, eng, "lead", "nonce-old")
	applyDeclaredRoles(ctx, eng, RolesConfig{
		Coordinator: []string{"lead"},
		Identity:    map[string]string{"lead": engine.RolePinFingerprint("nonce-old")},
	}, pins)
	if !holdsRole(t, eng, "lead", core.RoleCoordinator) {
		t.Fatal("setup: the declared coordinator was not granted")
	}

	// The config now names a credential the current holder does not have.
	applyDeclaredRoles(ctx, eng, RolesConfig{
		Coordinator: []string{"lead"},
		Identity:    map[string]string{"lead": engine.RolePinFingerprint("nonce-new")},
	}, pins)
	if holdsRole(t, eng, "lead", core.RoleCoordinator) {
		t.Fatal("the predecessor keeps coordinator after the config was repointed at " +
			"a successor's fingerprint: the old credential holds a role the file " +
			"no longer gives it")
	}
}

// A ROLE THIS MECHANISM DID NOT GRANT IS NOT ITS TO WITHDRAW.
//
// A person granted admin through the admin API, and the config never named
// the agent. Removing nothing from a file that never held it must not demote:
// there is no pin, so there is no proof this mechanism granted anything, and
// the human's decision stands (round nineteen already pins that direction for
// a REGRANT; this is the withdrawal direction).
func TestAHandGrantedRoleIsNotWithdrawnByTheConfig(t *testing.T) {
	eng, ctx := testEngine(t)
	pins := loadRolePins(t.TempDir())

	registerAgentAs(t, eng, "trusted", "nonce-t")
	if _, err := eng.GrantRoleByHuman(ctx, "trusted", core.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	applyDeclaredRoles(ctx, eng, RolesConfig{}, pins)
	if !holdsRole(t, eng, "trusted", core.RoleAdmin) {
		t.Fatal("a role a person granted by hand was withdrawn by a config that never " +
			"mentioned the agent")
	}
}

// A stale pin beside a role a DIFFERENT agent now holds legitimately drops
// the pin and leaves the role. The launch claim can hand coordinator to
// whoever comes first on a fresh board; if that agent's fingerprint is not
// the pinned one, this mechanism did not grant what it holds.
func TestAStalePinDoesNotDemoteALegitimateNewHolder(t *testing.T) {
	eng, ctx := testEngine(t)
	pins := loadRolePins(t.TempDir())

	// The mechanism once granted coordinator to "lead" under nonce-old, and
	// recorded it. That agent is gone; the pin remains.
	pins.Pins[core.RoleCoordinator] = map[string]string{"lead": engine.RolePinFingerprint("nonce-old")}

	// A different agent now holds the name and the role, granted the way the
	// launch claim grants it: NOT by a person, so nothing but the fingerprint
	// guard stands between it and a demotion. The first version of this test
	// used the human's API and passed with the guard deleted, because the
	// engine's own "a person's decision stands" was doing the work.
	registerAgentAs(t, eng, "lead", "nonce-other")
	if _, err := eng.GrantRole(ctx, "lead", core.RoleCoordinator); err != nil {
		t.Fatal(err)
	}
	applyDeclaredRoles(ctx, eng, RolesConfig{}, pins)
	if !holdsRole(t, eng, "lead", core.RoleCoordinator) {
		t.Fatal("an agent holding a role this mechanism never granted was demoted " +
			"because a stale pin under its NAME recorded a grant to somebody else")
	}
	if _, pinned := pins.Pins[core.RoleCoordinator]["lead"]; pinned {
		t.Error("the stale pin was kept, so the next pass judges the same agent " +
			"against the same wrong record")
	}
}

// A PERSON'S DECISION STANDS IN THE WITHDRAWAL DIRECTION TOO.
//
// The mechanism granted admin and pinned it; a person then confirmed that
// same role by hand during this run; the config drops the name. The engine
// declines the reconciler's REGRANT after a hand-made change (round
// nineteen), and it must decline the withdrawal the same way, or the config
// file overrules a person who acted after reading it.
func TestAWithdrawalDoesNotOverruleAPersonsDecision(t *testing.T) {
	eng, ctx := testEngine(t)
	pins := loadRolePins(t.TempDir())

	registerAgentAs(t, eng, "keeper", "nonce-k")
	applyDeclaredRoles(ctx, eng, RolesConfig{
		Admin:    []string{"keeper"},
		Identity: map[string]string{"keeper": engine.RolePinFingerprint("nonce-k")},
	}, pins)
	if !holdsRole(t, eng, "keeper", core.RoleAdmin) {
		t.Fatal("setup: not granted")
	}
	// The person says: yes, admin, on purpose.
	if _, err := eng.GrantRoleByHuman(ctx, "keeper", core.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	applyDeclaredRoles(ctx, eng, RolesConfig{}, pins)
	if !holdsRole(t, eng, "keeper", core.RoleAdmin) {
		t.Fatal("the config withdrew a role a person had set by hand during this run")
	}
}
