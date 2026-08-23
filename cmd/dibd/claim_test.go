package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
)

// A correct secret that the core then refuses must not spend the claim.
//
// The first real claim this code served did exactly that: the agent presented
// the right secret, verify matched it and deleted the file, and core refused
// the op because the claimant was ephemeral. The role was unheld, the secret was
// gone, and the only way to mint another was to restart the daemon. Holding the
// secret is one of the conditions, not all of them, so spending it cannot be the
// same step as checking it.
func TestARefusedClaimIsNotSpent(t *testing.T) {
	dir := t.TempDir()
	c := newCoordinatorClaim(dir, false)

	ok, spend := c.verify(c.secret)
	if !ok || spend == nil {
		t.Fatal("setup: the minted secret did not verify")
	}
	// The op is refused, so spend is never called: exactly what the engine does
	// when applyAndLedger returns an error.

	if again, _ := c.verify(c.secret); !again {
		t.Error("the claim was spent by a check alone; a refused claim now strands the board")
	}
	if _, err := os.Stat(filepath.Join(dir, "coordinator.claim")); err != nil {
		t.Errorf("the claim file was removed before the op succeeded: %v", err)
	}
}

// A claim that succeeds is spent, once.
func TestASucceededClaimIsSpentExactlyOnce(t *testing.T) {
	dir := t.TempDir()
	c := newCoordinatorClaim(dir, false)

	ok, spend := c.verify(c.secret)
	if !ok {
		t.Fatal("setup: the minted secret did not verify")
	}
	secret := c.secret
	spend()

	if again, _ := c.verify(secret); again {
		t.Error("the secret still works after being spent: a second agent can take the role")
	}
	if _, err := os.Stat(filepath.Join(dir, "coordinator.claim")); !os.IsNotExist(err) {
		t.Errorf("the claim file survived the claim: err = %v", err)
	}
}

// A wrong secret gets no spend function, so there is nothing to call.
func TestAWrongSecretCannotSpendTheClaim(t *testing.T) {
	dir := t.TempDir()
	c := newCoordinatorClaim(dir, false)

	if ok, spend := c.verify("not-the-secret"); ok || spend != nil {
		t.Fatal("a wrong secret verified")
	}
	if ok, _ := c.verify(c.secret); !ok {
		t.Error("a failed attempt invalidated the real secret")
	}
}

// A board that already has a coordinator mints nothing.
func TestNoClaimIsMintedWhenTheRoleIsHeld(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "coordinator.claim")
	if err := os.WriteFile(path, []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := newCoordinatorClaim(dir, true)

	if ok, _ := c.verify("stale"); ok {
		t.Error("a stale claim file was honoured against a board that settled the question")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the stale claim file was left readable: err = %v", err)
	}
}

// The claim must be refused once the board has a coordinator, through the real
// verification path.
//
// The engine clears a caller-supplied ClaimVerified and re-derives it from the
// claim file, so this is the only place the guard can be shown to be REACHED
// rather than merely correct. An attacker here is an ordinary agent that read
// coordinator.claim out of the data directory, which is same-user readable and
// documented to be: the secret is legitimate, and the board having a
// coordinator already is what must stop it.
func TestAClaimIsRefusedOnceTheRoleIsHeld(t *testing.T) {
	eng, ctx := testEngine(t)
	dir := t.TempDir()
	// ONE claim, minted and installed. Calling newCoordinatorClaim and
	// installCoordinatorClaim separately makes two, and the secret held here is
	// then not the one the engine verifies against: the claim is refused for the
	// wrong reason and the probe proves nothing.
	c := newCoordinatorClaim(dir, false)
	eng.SetCoordinatorClaim(c.verify)

	registerPersistent := func(name string) string {
		t.Helper()
		res, err := eng.Do(ctx, &core.Op{
			Kind: core.OpRegister, Name: name, Nonce: "n-" + name,
			AgentKind: core.KindPersistent,
		})
		if err != nil {
			t.Fatalf("setup: register %s: %v", name, err)
		}
		tok, _ := res["token"].(string)
		return tok
	}

	// The declared coordinator receives its role, the way the reconciler grants it.
	registerPersistent("fleet-lead")
	if _, err := eng.GrantRole(ctx, "fleet-lead", core.RoleCoordinator); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Another agent presents the genuine, unspent claim secret.
	other := registerPersistent("opportunist")
	_, err := eng.Do(ctx, &core.Op{
		Kind: core.OpClaimCoordinator, Token: other, Nonce: c.secret,
	})
	if err == nil {
		t.Fatal("a genuine launch claim was spent on a board that already had a " +
			"coordinator: a second agent now holds broadcast, force_release, " +
			"eviction and mailbox adoption")
	}
	if got, _ := eng.AgentRole(ctx, "opportunist"); got == core.RoleCoordinator {
		t.Error("the claim was refused and the role landed anyway")
	}
}

// A board that NAMES a coordinator is never offered a claim, even before that
// agent has registered.
//
// The claim is minted when the board has no coordinator, and on a fresh board
// that is true for as long as it takes the pinned agent to start: the declared
// pass runs before anybody has registered and grants nothing, and the
// reconciler waits for a later tick. In that gap `coordinator.claim` sat in the
// data directory for any same-user agent to read and spend. Later
// reconciliation grants the intended coordinator and does not demote the one
// that got there first, so a board ends with a coordinator its operator did not
// choose, holding broadcast, eviction, force-release and mailbox adoption.
//
// The existing test grants the legitimate coordinator BEFORE the opportunist
// presents the claim, so it exercises the already-settled board and cannot see
// this ordering at all. This one is the fresh board: nobody registered, nothing
// granted, exactly as at startup.
func TestAConfiguredCoordinatorIsNotRaceableBeforeItRegisters(t *testing.T) {
	dir := t.TempDir()

	// The state at `installCoordinatorClaim` on a fresh board: no coordinator
	// has been granted, because no agent exists to grant it to.
	const hasCoordinator = false
	// WITH AN IDENTITY, because that is what makes the declaration grantable.
	// A name on its own can never receive the role, so it decides nothing and
	// the board still needs its bootstrap claim; see the inert case below.
	declared := RolesConfig{
		Coordinator: []string{"fleet-lead"},
		Identity:    map[string]string{"fleet-lead": engine.RolePinFingerprint("n")},
	}

	c := newCoordinatorClaim(dir, coordinatorAlreadyDecided(hasCoordinator, declared))
	if c.secret != "" {
		t.Error("a board whose dibs.toml names a coordinator was still offered a " +
			"launch claim. The claim answers \"who coordinates here\", and the " +
			"operator has already answered it: minting one lets any agent that " +
			"can read the data directory take the role before the named identity " +
			"has even started")
	}
	if _, err := os.Stat(filepath.Join(dir, "coordinator.claim")); err == nil {
		t.Error("coordinator.claim was written for a board that names a coordinator")
	}

	// AND ADMIN COUNTS, because admin includes coordinator authority. A board
	// configured with only `[roles] admin = [...]` has named who coordinates
	// just as surely as one that spells it out; the first version of this fix
	// looked only at Coordinator, so that board still minted a claim an
	// opportunist could spend, and later reconciliation grants the admin
	// without demoting whoever took it.
	adminOnly := RolesConfig{
		Admin:    []string{"fleet-lead"},
		Identity: map[string]string{"fleet-lead": engine.RolePinFingerprint("n")},
	}
	if coordinatorAlreadyDecided(false, adminOnly) != true {
		t.Error("a board that declares an ADMIN was still offered a launch claim. " +
			"Admin carries coordinator authority, so the operator has answered the " +
			"question the claim asks, and an agent that reads the claim file first " +
			"keeps broadcast, eviction and force-release on a board it was never " +
			"meant to run")
	}

	// A NAME WITH NO IDENTITY IS INERT, and must not suppress the claim.
	//
	// A standing role needs a fingerprint in [roles.identity]; without one the
	// grant is refused forever rather than delayed. Reading the bare name as an
	// answer left a board with neither the standing authority its config
	// promised nor the bootstrap path that exists for its absence: no
	// coordinator, ever, and no way to take the role. The README's own copyable
	// example was exactly that file.
	if coordinatorAlreadyDecided(false, RolesConfig{Coordinator: []string{"nobody"}}) {
		t.Error("a coordinator declared with no [roles.identity] suppressed the " +
			"launch claim. That grant can never be made, so the declaration decides " +
			"nothing and the board is left with no coordinator and no way to get one")
	}

	// A board that names NOBODY still gets one: this must not disable the
	// bootstrap path it exists to provide.
	c2 := newCoordinatorClaim(t.TempDir(), coordinatorAlreadyDecided(false, RolesConfig{}))
	if c2.secret == "" {
		t.Error("a board with no declared coordinator was offered no claim, so a " +
			"fleet with no human at the keyboard can never have one at all")
	}
}
