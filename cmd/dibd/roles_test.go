package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

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
		// FINGERPRINTS, not nonces. dibs.toml holding a nonce would be the
		// operator's config handing out the agent's whole recovery credential
		// to anything running as them.
		Identity: map[string]string{
			"orchestrator": engine.RolePinFingerprint("nonce-orchestrator"),
			"fleet-lead":   engine.RolePinFingerprint("nonce-fleet-lead"),
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

	// AND THE ONE THAT HAS NOT REGISTERED YET, which is the case the comment
	// above this test is about and the reason production reapplies on a ticker.
	//
	// Both agents were registered before the first pass, so every assertion so
	// far describes a board where everybody is already present: the startup
	// order that actually happens, an operator's daemon coming up before their
	// agents do, went untested, and deleting the reconciliation loop would have
	// left this green.
	pins := loadRolePins(t.TempDir())
	late := RolesConfig{
		Coordinator: []string{"latecomer"},
		Identity:    map[string]string{"latecomer": engine.RolePinFingerprint("nonce-late")},
	}
	applyDeclaredRoles(ctx, eng, late, pins) // nobody by that name yet
	if holdsRole(t, eng, "latecomer", core.RoleCoordinator) {
		t.Fatal("a role was granted to an agent that has never registered, so the " +
			"grant is following the NAME again")
	}
	registerAgentAs(t, eng, "latecomer", "nonce-late")
	applyDeclaredRoles(ctx, eng, late, pins) // the ticker's next pass
	if !holdsRole(t, eng, "latecomer", core.RoleCoordinator) {
		t.Error("an agent that registered after startup never received its declared " +
			"role. The daemon comes up before the agents do, so this is the ordinary " +
			"order rather than an edge case, and without a later pass the operator's " +
			"config silently applies to nobody")
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
		Admin: []string{"release-manager"},
		Identity: map[string]string{
			"release-manager": engine.RolePinFingerprint("the-real-one"),
		},
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
//
// WITH AN IDENTITY CONFIGURED, which is what makes this about the nonce. The
// first version left [roles.identity] empty as well, so the grant was refused
// for having no configured fingerprint and the nonce rule could have been
// deleted outright with this still green: two protections, one assertion, and
// the wrong one doing the work.
//
// The configured identity is a REAL fingerprint, of some nonce the operator
// chose. RolePinFingerprint("") is itself empty, so pinning that would recreate
// the same masking with an extra step.
func TestADeclaredRoleNeedsAnAgentThatCanProveItselfTomorrow(t *testing.T) {
	eng, ctx := testEngine(t)
	if _, err := eng.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "no-nonce"}); err != nil {
		t.Fatal(err)
	}
	intended := engine.RolePinFingerprint("the-nonce-the-operator-chose")
	if intended == "" {
		t.Fatal("the configured identity is empty, so the refusal below would be " +
			"about the missing config rather than about the agent")
	}
	cfg := RolesConfig{
		Admin:    []string{"no-nonce"},
		Identity: map[string]string{"no-nonce": intended},
	}
	applyDeclaredRoles(ctx, eng, cfg, loadRolePins(t.TempDir()))
	if holdsRole(t, eng, "no-nonce", core.RoleAdmin) {
		t.Error("an agent with no nonce was granted a standing role: it cannot prove " +
			"it is itself after a restart, so the role would pass to whoever takes " +
			"the name next")
	}

	// And the same configuration DOES grant to an agent that has one, so this
	// cannot pass by refusing everything.
	registerAgentAs(t, eng, "has-nonce", "a-durable-secret")
	ok := RolesConfig{
		Admin:    []string{"has-nonce"},
		Identity: map[string]string{"has-nonce": engine.RolePinFingerprint("a-durable-secret")},
	}
	applyDeclaredRoles(ctx, eng, ok, loadRolePins(t.TempDir()))
	if !holdsRole(t, eng, "has-nonce", core.RoleAdmin) {
		t.Fatal("no agent can hold a declared role at all, so the refusal above says " +
			"nothing about nonces")
	}

	// THE NONCE RULE ON ITS OWN.
	//
	// Several protections deliver the property above and any of them will
	// refuse a nonce-less agent, so the whole-path assertion cannot say WHICH
	// one is doing it: with no identity configured the missing-config branch
	// answers, and with one configured the mismatch branch does. Both are
	// correct and neither is this rule. Asked directly, the empty fingerprint
	// is the only thing wrong.
	for _, want := range []struct{ name, configured string }{
		{"with no identity configured", ""},
		{"with an identity configured", intended},
	} {
		t.Run("the nonce rule "+want.name, func(t *testing.T) {
			err := loadRolePins(t.TempDir()).check(core.RoleAdmin, "no-nonce", "", want.configured)
			if err == nil {
				t.Fatal("an agent with no durable identity was accepted for a standing role")
			}
			if !strings.Contains(err.Error(), "no nonce") {
				t.Errorf("refused for a different reason, so this says nothing about "+
					"the nonce rule: %v", err)
			}
		})
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
			Admin: []string{"fleet-lead"},
			Identity: map[string]string{
				"fleet-lead": engine.RolePinFingerprint("the-nonce-the-operator-chose"),
			},
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

// The corrective errors on the role-pin path must not talk an operator into
// making things worse.
//
// PHILOSOPHY's honesty rule says every error carries the corrective call. Two
// on this path named the wrong one, and both are security-relevant:
//
//   - The "no identity configured" refusal said to add `<that agent's nonce>`
//     to [roles.identity]. A 256-bit nonce is 64 hex characters, exactly the
//     shape the fingerprint check accepts, so the file loaded and nothing ever
//     objected. Anything that could read dibs.toml could then register with
//     that name and that nonce and be handed the agent's own token, mailbox and
//     role: an upgrade from a two-minute startup race to a standing key. The
//     startup log already printed the correct line, so the two contradicted
//     each other and the dangerous one came first.
//
//   - The mismatch refusal offered a handover repair of one step. Both the pin
//     file and dibs.toml are read once at startup, so that step changes nothing
//     under a running daemon and still fails on the next boot against the old
//     fingerprint. An error naming a fix that does not fix anything spends the
//     operator's trust in every other error this daemon prints.
//
// Prose is not checkable by reading, so it is checked here.
func TestTheRolePinErrorsNameTheCorrectiveThatWorks(t *testing.T) {
	t.Run("the first-pin refusal never asks for the nonce", func(t *testing.T) {
		p := loadRolePins(t.TempDir())
		err := p.check("fleet-lead", core.RoleAdmin, engine.RolePinFingerprint("n"), "")
		if err == nil {
			t.Fatal("a declared role with no [roles.identity] was accepted, so this " +
				"check is reading an error that no longer exists")
		}
		assertDoesNotAskForANonce(t, "the first-pin refusal", err)
		if !strings.Contains(strings.ToLower(err.Error()), "fingerprint") {
			t.Errorf("the refusal does not say to use the FINGERPRINT:\n  %s", err)
		}
	})

	// The MISMATCH refusal too, which is the one an operator reads while
	// debugging a role that stopped working, and which said the longest for
	// "the nonce [roles.identity] names for it". The first version of this
	// guard listed two literal phrases and walked straight past that sentence:
	// a check that knows only the spellings already fixed is a check that finds
	// nothing new, which is this repository's most-repeated lesson about
	// guarding prose.
	t.Run("the identity-mismatch refusal never asks for the nonce", func(t *testing.T) {
		// UNPINNED, which is the branch that carried the wording. A role that
		// is already pinned takes the handover path instead; the first version
		// of this subtest established a pin and so never reached the sentence
		// it was written for, and passed against it.
		p := loadRolePins(t.TempDir())
		err := p.check(core.RoleAdmin, "fleet-lead",
			engine.RolePinFingerprint("the agent that turned up"),
			engine.RolePinFingerprint("the agent the operator meant"))
		if err == nil {
			t.Fatal("an agent whose fingerprint is not the one [roles.identity] " +
				"names was granted the role")
		}
		if strings.Contains(err.Error(), "cannot be read") ||
			strings.Contains(err.Error(), "has no nonce") {
			t.Fatalf("this reached a different refusal, so the wording under test "+
				"was never produced: %v", err)
		}
		assertDoesNotAskForANonce(t, "the identity-mismatch refusal", err)
	})

	t.Run("the handover repair names every step it needs", func(t *testing.T) {
		dir := t.TempDir()
		p := loadRolePins(dir)
		// An established pin, then a different agent under the same name.
		if err := p.check("fleet-lead", core.RoleAdmin,
			engine.RolePinFingerprint("the-original"), engine.RolePinFingerprint("the-original")); err != nil {
			t.Fatalf("establishing the pin: %v", err)
		}
		err := p.check("fleet-lead", core.RoleAdmin,
			engine.RolePinFingerprint("a-successor"), engine.RolePinFingerprint("the-original"))
		if err == nil {
			t.Fatal("a different agent under a pinned name was accepted, so there is " +
				"no mismatch error to check")
		}
		got := strings.ToLower(err.Error())
		for _, want := range []string{"roles.identity", "restart"} {
			if !strings.Contains(got, want) {
				t.Errorf("the handover repair does not mention %q:\n  %s\n"+
					"Removing the pin alone cannot work: both files are read once at "+
					"startup, so the running daemon keeps its loaded copy and the next "+
					"boot still checks the successor against the old fingerprint",
					want, err.Error())
			}
		}
	})
}

// assertDoesNotAskForANonce fails if an error points the operator at
// [roles.identity] and calls what belongs there a nonce.
//
// ONE PLACE, and it matches on the RELATION rather than on remembered wording.
// The first version of this check listed the two exact phrases that had already
// been fixed, so the next occurrence -- "did not present the nonce
// [roles.identity] names for it" -- passed it untouched, in the error an
// operator is most likely to be reading when they decide what to put in that
// field. A nonce is the agent's whole recovery credential and has the same
// 64-hex shape as a fingerprint, so a file that holds one loads silently and
// hands that agent to anything running as the operator.
func assertDoesNotAskForANonce(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: no error to inspect, so this check verified nothing", what)
	}
	got := err.Error()
	lower := strings.ToLower(got)
	// The hazard is the two ideas in one sentence: this field, and that word.
	if strings.Contains(lower, "nonce") && strings.Contains(got, "[roles.identity]") {
		// Saying "never the nonce" is the correct use and must stay allowed.
		for _, safe := range []string{"never its nonce", "never the nonce", "not the nonce"} {
			if strings.Contains(lower, safe) {
				return
			}
		}
		t.Errorf("%s describes [roles.identity] as holding a nonce:\n  %s\n"+
			"That field takes a FINGERPRINT. A nonce is the agent's whole recovery "+
			"credential and is the same 64-hex shape, so an operator who follows this "+
			"puts it in a file anything running as them can read, and whatever reads "+
			"it can register as that agent and take its token, mailbox and role",
			what, got)
	}
}

// A nonce pasted into [roles.identity] is caught, and named as an exposure.
//
// A 256-bit nonce is 64 lowercase hex characters, exactly the shape of a
// fingerprint, so no validator reading dibs.toml on its own can tell them
// apart: the shape check accepts both, the board comes up, and nothing ever
// says the value is wrong. The regression case in boardconfig used
// "the-secret-nonce", which is neither 64 characters nor hex, so it only proved
// that obviously malformed values fail.
//
// Where the grant is decided both values exist, which makes the test exact:
// hashing what the operator wrote gives this agent's fingerprint only if what
// they wrote was this agent's nonce.
func TestANoncePastedIntoTheIdentityTableIsRefusedAsAnExposure(t *testing.T) {
	// The realistic representation, and the one the old case could not model.
	nonce := strings.Repeat("a1b2c3d4", 8) // 64 lowercase hex
	if len(nonce) != 64 {
		t.Fatalf("fixture is %d characters, not the 64 a 256-bit nonce has", len(nonce))
	}
	fp := engine.RolePinFingerprint(nonce)

	p := loadRolePins(t.TempDir())
	err := p.check(core.RoleAdmin, "fleet-lead", fp, nonce)
	if err == nil {
		t.Fatal("a raw nonce in [roles.identity] was accepted as an identity. It is " +
			"the agent's whole recovery credential: anything running as the operator " +
			"that reads dibs.toml can register with that name and nonce and be handed " +
			"the agent's token, mailbox and role")
	}
	got := strings.ToLower(err.Error())
	if !strings.Contains(got, "nonce") {
		t.Errorf("the refusal does not say the value is a nonce:\n  %s", err)
	}
	// Refusing is not the whole remedy: the credential is already in a file.
	if !strings.Contains(got, "new nonce") && !strings.Contains(got, "rotate") {
		t.Errorf("the refusal does not tell the operator to rotate the nonce:\n  %s\n"+
			"Correcting the file leaves a credential that has been sitting on disk", err)
	}

	// And the correct value still works, so this cannot pass by refusing
	// everything.
	if err := p.check(core.RoleAdmin, "fleet-lead", fp, fp); err != nil {
		t.Errorf("a correct fingerprint was refused: %v", err)
	}
}

// A corrupt pin file must say WHY every grant is refused.
//
// Refusing is the safe direction and was never in doubt. But the parse error
// was dropped on the floor, so `check` reported that the file "cannot be read
// (<nil>)": an operator whose declared roles have all stopped working is told
// the file is unreadable, given no reason, and sent to look at permissions on a
// file whose permissions are fine. readErr is the field that error prints, and
// its own contract says it holds why. Existing coverage used an OS-level read
// failure, which is the one case that already populated it.
func TestACorruptPinFileExplainsItself(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "roles.pinned"),
		[]byte("{ this is not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := loadRolePins(dir)
	if p.Pins != nil {
		t.Fatal("a corrupt pin file left the pins usable, so a damaged file " +
			"silently un-pins every role: the failure the file exists to prevent")
	}
	err := p.check(core.RoleAdmin, "fleet-lead", engine.RolePinFingerprint("n"), "")
	if err == nil {
		t.Fatal("a grant was allowed against an unreadable pin file")
	}
	if strings.Contains(err.Error(), "<nil>") {
		t.Errorf("the refusal reports no reason:\n  %s\n"+
			"The operator sees every declared role stop working and is told the "+
			"file cannot be read, with the parse error that would explain it "+
			"thrown away", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "json") &&
		!strings.Contains(err.Error(), "invalid character") {
		t.Errorf("the refusal does not name the parse failure:\n  %s", err)
	}
}

// The one instruction this feature has must be valid TOML.
//
// A standing role needs the agent's fingerprint in `[roles.identity]`, and the
// operator cannot look that up anywhere else, so the daemon prints the line to
// paste. It interpolated the name as a bare key, and a bare TOML key holds only
// letters, digits, underscores and dashes: for `Fleet Lead`, which is exactly
// the kind of name the reconciler had just learned to resolve, the printed
// snippet does not parse. Following the daemon's own advice therefore produced
// a dibs.toml it then refuses to load, with the role still ungranted and a new
// fault on top.
//
// PARSED, not pattern-matched. A check that the key "looks quoted" would pass
// against a quoting scheme TOML does not accept; this hands the snippet to the
// same decoder the daemon uses and reads the value back out.
func TestThePrintedIdentityPinIsValidTOML(t *testing.T) {
	const fp = "0f9c1c2b3a4d5e6f70819293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7"
	for _, name := range []string{
		"fleet-lead",   // already a bare key
		"Fleet Lead",   // the documented display-name form
		"ops/west",     // a slash, which register slugs to a dash
		"lead_2",       // underscores are bare-legal and must not be quoted away
		`say "hello"`,  // quotes in the name must not escape the key
		"agent\\north", // a backslash, which TOML treats as an escape
	} {
		snippet := "[roles.identity]\n" + tomlKey(name) + " = " + strconv.Quote(fp) + "\n"
		var got struct {
			Roles struct {
				Identity map[string]string `toml:"identity"`
			} `toml:"roles"`
		}
		if _, err := toml.Decode(snippet, &got); err != nil {
			t.Errorf("the daemon tells an operator to paste this and it does not "+
				"parse:\n%s\n%v\n\nThe role stays ungranted and dibs.toml now fails to "+
				"load, which is worse than the state it was meant to fix", snippet, err)
			continue
		}
		if got.Roles.Identity[name] != fp {
			t.Errorf("the snippet parses but pins %q rather than %q:\n%s",
				keysOf(got.Roles.Identity), name, snippet)
		}
	}

	// And a name that needs no quoting does not get any: an operator copies
	// what they are shown, and a needless quote is a pattern they will repeat
	// where it is wrong.
	if got := tomlKey("fleet-lead"); got != "fleet-lead" {
		t.Errorf("a bare-legal name was rendered as %s", got)
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// A pin records who took a role. It is not permission to keep it.
//
// The established-pin branch returned success the moment the stored pin matched
// the live agent, without looking at what the operator currently authorises. So
// both revocations did nothing: deleting an agent from `[roles.identity]` left
// it holding the role, and pointing that entry at a successor left the old
// credential accepted beside it. On the next restart the reconciler passed the
// new configuration in, was told yes, and re-granted the old identity. For
// admin that is every decrypted mailbox on the board, restored against the
// operator's written instruction.
//
// SECURITY.md says `[roles.identity]` names the agent allowed to hold the role.
// That has to be true at every grant, not only at the first one.
//
// The existing tests cover an unpinned mismatch and a DIFFERENT live agent under
// an old pin. Neither covers the case where the pin still matches the agent and
// the configuration no longer does, which is what revocation looks like.
func TestAnEstablishedPinDoesNotOutrankTheCurrentConfiguration(t *testing.T) {
	const (
		held      = "1111111111111111111111111111111111111111111111111111111111111111"
		successor = "2222222222222222222222222222222222222222222222222222222222222222"
	)
	// A legitimately established pin: config and agent agreed at the time.
	fresh := func(t *testing.T) *rolePins {
		t.Helper()
		p := loadRolePins(t.TempDir())
		if err := p.check(core.RoleAdmin, "fleet-lead", held, held); err != nil {
			t.Fatalf("setup: the first grant was refused (%v), so nothing below is "+
				"about an established pin", err)
		}
		return p
	}

	t.Run("the operator withdraws the authorisation", func(t *testing.T) {
		p := fresh(t)
		// [roles.identity] no longer names this agent.
		if err := p.check(core.RoleAdmin, "fleet-lead", held, ""); err == nil {
			t.Error("admin was re-granted to an agent the operator has removed from " +
				"[roles.identity]. Deleting the entry is the obvious way to revoke a " +
				"standing role, and it did nothing: on the next restart the same " +
				"credential is handed the god view again")
		}
	})

	t.Run("the operator names a successor", func(t *testing.T) {
		p := fresh(t)
		// [roles.identity] points at somebody else; the predecessor is still
		// the agent registered under that name.
		if err := p.check(core.RoleAdmin, "fleet-lead", held, successor); err == nil {
			t.Error("admin was re-granted to the predecessor while [roles.identity] " +
				"names a successor's fingerprint. The handover the operator wrote down " +
				"has not happened, and the old credential keeps every mailbox")
		}
	})

	t.Run("and the ordinary case still works", func(t *testing.T) {
		p := fresh(t)
		if err := p.check(core.RoleAdmin, "fleet-lead", held, held); err != nil {
			t.Errorf("the agent the operator names, holding the pin it was granted "+
				"under, was refused: %v. Every reconciliation tick runs this, so a "+
				"board would lose its admin fifteen seconds after start", err)
		}
	})
}
