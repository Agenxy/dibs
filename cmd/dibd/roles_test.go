package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
)

// A role declared in the config is granted at startup: including to an agent that
// has not registered yet.
//
// The ordering is the whole point. A standing coordinator that only took effect
// if it happened to be running when the daemon booted is not standing; and since
// a hand-granted role dies with the ledger it lived in, a board reset would
// silently leave the fleet with nobody able to merge two colliding agents.
func TestRolesDeclaredInConfigAreGrantedAtStartup(t *testing.T) {
	eng, ctx := testEngine(t)

	// The agents must exist: a role attaches to an agent, and core answers
	// E_NO_AGENT otherwise. That is exactly why the real path re-applies on a
	// ticker instead of granting once at startup.
	registerAgent(t, eng, "orchestrator")
	registerAgent(t, eng, "fleet-lead")
	applyDeclaredRoles(ctx, eng, RolesConfig{
		Coordinator: []string{"orchestrator"},
		Admin:       []string{"fleet-lead"},
		// The operator names WHICH agent, not just the name: a declared role
		// with no identity behind it is granted to nobody, because the first
		// agent to take a name is not necessarily the one that was meant.
		Identity: map[string]string{
			"orchestrator": "nonce-orchestrator",
			"fleet-lead":   "nonce-fleet-lead",
		},
	}, loadRolePins(t.TempDir()))

	for agent, want := range map[string]string{
		"orchestrator": core.RoleCoordinator,
		"fleet-lead":   core.RoleAdmin,
	} {
		if !holdsRole(t, eng, agent, want) {
			t.Errorf("%s does not hold %q after applyDeclaredRoles: a role written in "+
				"dibs.toml that does not take effect is a config that lies", agent, want)
		}
	}
}

// An empty [roles] table must grant nothing.
//
// Otherwise the default install quietly hands somebody the god view, which is
// the opposite of the property this whole path is built around.
func TestNoRolesDeclaredGrantsNothing(t *testing.T) {
	eng, ctx := testEngine(t)
	applyDeclaredRoles(ctx, eng, RolesConfig{}, loadRolePins(t.TempDir()))
	registerAgent(t, eng, "orchestrator")
	if holdsRole(t, eng, "orchestrator", core.RoleCoordinator) {
		t.Error("an agent was granted coordinator from an EMPTY [roles] table; the default " +
			"install must hand nobody breadth it did not ask for")
	}
}

// A name that does not exist, or is empty, must not stop the daemon.
//
// A board is more useful than a perfectly-validated config file: refusing to
// start over one typo leaves the fleet with nowhere to coordinate, and the
// operator unable to read the complaint.
func TestABadRoleEntryDoesNotStopTheDaemon(t *testing.T) {
	eng, ctx := testEngine(t)
	applyDeclaredRoles(ctx, eng, RolesConfig{Coordinator: []string{"", "nobody-here"}},
		loadRolePins(t.TempDir()))
	// Reaching this line without panicking or hanging is the assertion: a typo
	// in the config must not take the daemon down with it.
}

// registerAgent registers with a NONCE, because a declared role now requires
// one: an agent with no nonce cannot prove it is itself after a restart, and a
// standing role is precisely a thing that outlives restarts.
func registerAgent(t *testing.T, eng *engine.Engine, name string) {
	t.Helper()
	registerAgentAs(t, eng, name, "nonce-"+name)
}

func registerAgentAs(t *testing.T, eng *engine.Engine, name, nonce string) {
	t.Helper()
	if _, err := eng.Do(context.Background(),
		&core.Op{Kind: core.OpRegister, Name: name, Nonce: nonce}); err != nil {
		t.Fatalf("registering %s: %v", name, err)
	}
}

func testEngine(t *testing.T) (*engine.Engine, context.Context) {
	t.Helper()
	dir := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "test", box)
	if err != nil {
		t.Fatal(err)
	}
	st := core.NewState("test", core.DefaultLimits())
	if _, err := led.Replay(st); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(st, led, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go eng.Run(ctx)
	t.Cleanup(func() { cancel(); _ = led.Close() })
	return eng, ctx
}

// holdsRole asks the state machine rather than parsing a board rendering.
//
// Re-granting a role an agent already holds reports changed:false, which is
// exactly the question "is it already set", and it does not depend on how the
// board happens to serialise itself today. An earlier version of this helper
// read the board payload and silently returned "" for a role that HAD been
// granted, which made a passing implementation look broken.
// It READS the role now. It used to ask by calling GrantRole and inferring the
// answer from `changed`, which meant a probe for "is this agent admin?" made it
// admin and then reported that it was not, because the grant had changed
// something. Every authorization assertion in this file rested on that.
// Raised by the pre-release review.
func holdsRole(t *testing.T, eng *engine.Engine, agent, role string) bool {
	t.Helper()
	got, err := eng.AgentRole(context.Background(), agent)
	if err != nil {
		return false
	}
	return got == role
}

// Config can grant a role. An AGENT still cannot.
//
// This is the property the whole feature is balanced against: the operator's
// file is a human decision, but nothing reachable from an agent may promote
// anything. If grant_role ever became callable with an agent token: directly, or
// by some tool growing a role argument: an agent could hand itself the god view
// and read every other agent's mail.
//
// Asserted here rather than trusted to the HTTP gate, because the gate is one
// layer and this is the invariant underneath it.
func TestAnAgentStillCannotPromoteItself(t *testing.T) {
	eng, _ := testEngine(t)
	reg, err := eng.Do(context.Background(), &core.Op{Kind: core.OpRegister, Name: "climber"})
	if err != nil {
		t.Fatal(err)
	}
	token, _ := reg["token"].(string)
	if token == "" {
		t.Fatal("no token from register; this check would be vacuous")
	}

	// An agent-authenticated grant_role must be refused. The engine treats
	// grant_role as a system op admitted only on the admin path, so presenting a
	// token must not make it work.
	// The op must be REFUSED, and the role must not land. Asserting only the
	// second, inside an `if err == nil`, let a regression that returns success
	// while doing nothing pass silently: nothing then distinguishes "refused"
	// from "accepted and ineffective", and the second is one refactor away from
	// being effective.
	if _, err := eng.Do(context.Background(), &core.Op{
		Kind: core.OpGrantRole, To: "climber", Mode: core.RoleAdmin, Token: token,
	}); err == nil {
		t.Error("an agent-authenticated grant_role was ACCEPTED. Even if it changed " +
			"nothing today, a system op that takes an agent token is one refactor " +
			"from promoting whoever calls it")
	}
	if holdsRole(t, eng, "climber", core.RoleAdmin) {
		t.Fatal("an agent promoted ITSELF to admin using its own agent token. " +
			"that role can read every other agent's mail")
	}

	// And it is not quietly a coordinator either.
	if holdsRole(t, eng, "climber", core.RoleCoordinator) {
		t.Error("an agent acquired coordinator without the admin path")
	}
}

// A declared role follows an IDENTITY, not a name.
//
// `[roles] admin = ["release-manager"]` authorises a string, and any agent may
// register under any free name. So an ordinary agent that could read dibs.toml,
// or guess the name, registered as `release-manager` and the reconciler handed
// it admin: the god view over every decrypted mailbox, while SECURITY.md and
// docs/CONFIGURATION.md both promised "no agent can promote itself". It did not
// promote itself; it only had to be called the right thing. Reproduced against a
// live daemon, and present in v0.0.5 and v0.0.6.
//
// This drives applyDeclaredRoles, not the pin store alone: the property is that
// the RECONCILER refuses, and a test of the helper would pass while the caller
// went on granting.
func TestADeclaredRoleWillNotFollowAStolenName(t *testing.T) {
	eng, ctx := testEngine(t)
	pinDir := t.TempDir()
	// The operator names the identity as well as the name, which is what lets
	// the FIRST grant be verified rather than merely recorded.
	cfg := RolesConfig{
		Admin:    []string{"release-manager"},
		Identity: map[string]string{"release-manager": "the-real-one"},
	}

	// The agent the operator meant takes the role, and is pinned to it.
	registerAgentAs(t, eng, "release-manager", "the-real-one")
	applyDeclaredRoles(ctx, eng, cfg, loadRolePins(pinDir))
	if !holdsRole(t, eng, "release-manager", core.RoleAdmin) {
		t.Fatal("setup: the intended agent did not receive the declared role, so the " +
			"theft below proves nothing")
	}

	// It goes away, and its name with it: a prune, a wiped ledger, a fresh
	// board. Anyone may now register under that name.
	impostor, impCtx := testEngine(t)
	registerAgentAs(t, impostor, "release-manager", "somebody-else")
	applyDeclaredRoles(impCtx, impostor, cfg, loadRolePins(pinDir))

	if holdsRole(t, impostor, "release-manager", core.RoleAdmin) {
		t.Error("an agent that merely took the configured NAME was granted admin, " +
			"which reads every mailbox on the board. A standing role has to follow " +
			"the identity it was granted to")
	}
}

// And the pin must not be satisfiable by an agent with no durable identity.
func TestADeclaredRoleNeedsAnAgentThatCanProveItselfTomorrow(t *testing.T) {
	eng, ctx := testEngine(t)
	if _, err := eng.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "no-nonce"}); err != nil {
		t.Fatal(err)
	}
	applyDeclaredRoles(ctx, eng, RolesConfig{Admin: []string{"no-nonce"}},
		loadRolePins(t.TempDir()))
	if holdsRole(t, eng, "no-nonce", core.RoleAdmin) {
		t.Error("an agent with no nonce was granted a standing role: it cannot prove " +
			"it is itself after a restart, so the role would pass to whoever takes " +
			"the name next")
	}
}

// Pin state that cannot be recorded must refuse the grant, not proceed.
//
// Two fail-open paths, both found by the pre-release review in the fix that was
// itself closing a privilege escalation:
//
//   - loadRolePins treated EVERY read error as "no pins yet", so a permissions
//     problem re-opened every declared role to whoever held its name.
//   - check recorded the fingerprint in memory before save() succeeded, so a
//     failed write left it there: the first reconciliation refused, and the next
//     one fifteen seconds later matched against its own unsaved value and
//     granted admin with nothing durable behind it.
//
// A security decision that survives only in memory is one that disappears on
// restart while behaving as though it had not.
func TestUnrecordablePinsRefuseTheGrant(t *testing.T) {
	t.Run("unreadable pin file", func(t *testing.T) {
		dir := t.TempDir()
		// A directory where the file belongs: readable as an entry, never as a file.
		if err := os.Mkdir(filepath.Join(dir, "roles.pinned"), 0o700); err != nil {
			t.Fatal(err)
		}
		pins := loadRolePins(dir)
		// The LOAD's own decision, not the write that happens to fail after it.
		// The first version of this asserted on check() and passed against the
		// fail-open, because the same fixture was unwritable too and save()
		// caught it by accident: a probe that is right for the wrong reason.
		if pins.Pins != nil {
			t.Error("pins that cannot be read were loaded as an empty store, which is " +
				"indistinguishable from a fresh board and re-opens every declared role")
		}
		if err := pins.check("admin", "release-manager", "fingerprint", "fingerprint"); err == nil {
			t.Error("a grant was allowed although the pin state could not be read")
		}
	})

	t.Run("unwritable pin file", func(t *testing.T) {
		dir := t.TempDir()
		pins := loadRolePins(dir)
		// Nothing can be created in a directory that is not writable.
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

		if err := pins.check("admin", "release-manager", "fingerprint", "fingerprint"); err == nil {
			t.Fatal("a grant was allowed although its pin could not be written")
		}
		// And the SECOND attempt must refuse too. This is the whole finding: the
		// first refusal left the fingerprint in memory, so the next tick matched
		// against it and granted.
		if err := pins.check("admin", "release-manager", "fingerprint", "fingerprint"); err == nil {
			t.Error("the second reconciliation matched against a fingerprint that was " +
				"never persisted, and granted the role")
		}
	})
}

// A fresh board must not hand a declared role to whoever registers first.
//
// The pin file held every LATER impostor to the first registrant's identity and
// asked the first one nothing. So an agent that read dibs.toml, or guessed that
// `admin = ["fleet-lead"]` is a likely line, could register under that name
// before the operator's own agent came up, become the durable pin, and be
// granted the god view: every agent's mail included. The two-minute window made
// that a race rather than a standing offer, which is not the same as making it
// safe.
//
// Found by the pre-release review, which also explained why neither existing
// test could catch it: TestAnAgentStillCannotPromoteItself drives OpGrantRole
// directly, and TestADeclaredRoleWillNotFollowAStolenName starts from a pin
// that is already legitimately established. Both skip the first grant, which is
// the one that was unguarded.
func TestAFreshBoardWillNotGrantADeclaredRoleToWhoeverAsksFirst(t *testing.T) {
	t.Run("no identity configured: nobody is granted", func(t *testing.T) {
		eng, ctx := testEngine(t)
		cfg := RolesConfig{Admin: []string{"fleet-lead"}}
		registerAgentAs(t, eng, "fleet-lead", "an-impostors-own-nonce")
		applyDeclaredRoles(ctx, eng, cfg, loadRolePins(t.TempDir()))
		if holdsRole(t, eng, "fleet-lead", core.RoleAdmin) {
			t.Error("the first agent to register under a declared name was granted " +
				"admin on a fresh board, so reading dibs.toml is enough to take the " +
				"god view and every agent's mail with it")
		}
	})

	t.Run("an identity configured: only that agent is granted", func(t *testing.T) {
		eng, ctx := testEngine(t)
		cfg := RolesConfig{
			Admin:    []string{"fleet-lead"},
			Identity: map[string]string{"fleet-lead": "the-nonce-the-operator-chose"},
		}
		pinDir := t.TempDir()

		// The impostor knows the NAME, which is all dibs.toml would tell it.
		registerAgentAs(t, eng, "fleet-lead", "an-impostors-own-nonce")
		applyDeclaredRoles(ctx, eng, cfg, loadRolePins(pinDir))
		if holdsRole(t, eng, "fleet-lead", core.RoleAdmin) {
			t.Fatal("an agent that took the declared name without the declared nonce " +
				"was granted admin")
		}

		// The operator's own agent, holding the nonce the operator chose.
		real, realCtx := testEngine(t)
		registerAgentAs(t, real, "fleet-lead", "the-nonce-the-operator-chose")
		applyDeclaredRoles(realCtx, real, cfg, loadRolePins(pinDir))
		if !holdsRole(t, real, "fleet-lead", core.RoleAdmin) {
			t.Error("the agent the operator named, presenting the nonce the operator " +
				"configured, was NOT granted its declared role: the check refuses " +
				"everybody, which is a broken feature rather than a closed hole")
		}
	})
}
