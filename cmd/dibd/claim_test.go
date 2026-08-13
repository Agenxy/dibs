package main

import (
	"os"
	"path/filepath"
	"testing"
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
