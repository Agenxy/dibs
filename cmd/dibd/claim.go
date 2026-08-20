package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/agenxy/dibs/internal/engine"
)

// coordinatorClaim is the bootstrap secret for the launch-time coordinator
// claim (SPEC §10).
//
// A fleet with no human at the keyboard could never have a coordinator: roles
// were granted only through the admin path, so force_release, close_space and
// clearing another agent's debris were permanently out of reach. This is how
// the agent that started the daemon takes the role.
//
// The secret lives in the data directory, 0600, beside the local secret the
// same agents already read to talk to us at all. That is deliberate and it is
// not a claim of strong isolation: SECURITY.md is explicit that every agent
// shares one coordination secret and that agent-to-agent isolation is a bar,
// not a wall. What this buys is that the role is TAKEN, once, deliberately, by
// something that could read the daemon's own directory, instead of being
// assumed by whoever asks first.
type coordinatorClaim struct {
	path string

	mu     sync.Mutex
	secret string // emptied once claimed, so the claim is single-use
}

// newCoordinatorClaim mints one unless the board already has a coordinator.
func newCoordinatorClaim(dir string, alreadyHas bool) *coordinatorClaim {
	path := filepath.Join(dir, "coordinator.claim")
	if alreadyHas {
		// Nothing to bootstrap. Remove a stale file so a claim cannot be made
		// against a board that already settled the question.
		_ = os.Remove(path)
		return &coordinatorClaim{path: path}
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		slog.Warn("could not mint a coordinator claim", "err", err)
		return &coordinatorClaim{path: path}
	}
	secret := hex.EncodeToString(b)
	if err := os.WriteFile(path, []byte(secret+"\n"), 0o600); err != nil {
		slog.Warn("could not write the coordinator claim", "path", path, "err", err)
		return &coordinatorClaim{path: path}
	}
	// "the agent that started this daemon" describes nobody under a service
	// manager: launchd started it, and no agent did. The mechanism was always
	// right, since any agent that can read the file may claim, and only the
	// sentence was wrong. Reported by an operator running it under launchd,
	// which is the arrangement the project recommends.
	slog.Info("no coordinator on this board; the first agent that reads the claim file can take it",
		"how", "claim_coordinator(nonce: the contents of "+path+")")
	return &coordinatorClaim{path: path, secret: secret}
}

// verify reports whether presented is the minted secret, in constant time, and
// returns the function that spends it.
//
// Checking and spending are separate because holding the secret is not the only
// thing a claim has to survive: core refuses the op if the claimant is
// ephemeral, closed, or already holds a role, and a claim spent on a refusal is
// gone with nobody holding the role and no way to mint another short of
// restarting the daemon. That is not a hypothetical either. It is what happened
// on the first real claim this code ever served.
func (c *coordinatorClaim) verify(presented string) (bool, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.secret == "" || presented == "" {
		return false, nil
	}
	if subtle.ConstantTimeCompare([]byte(c.secret), []byte(presented)) != 1 {
		return false, nil
	}
	return true, c.spend
}

// spend consumes the claim, once the op it authorised has been ledgered.
//
// Single use: the file goes so a second agent cannot take the role by reading a
// secret left lying about. Nothing guards the window between verify and spend
// because there is none to guard: both run on the engine's single writer, with
// the apply between them.
func (c *coordinatorClaim) spend() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.secret = ""
	if err := os.Remove(c.path); err != nil && !os.IsNotExist(err) {
		slog.Warn("claimed coordinator but could not remove the claim file",
			"path", c.path, "err", err)
	}
	slog.Info("coordinator claimed by the agent that started this daemon")
}

// installCoordinatorClaim wires the claim to the engine.
func installCoordinatorClaim(eng *engine.Engine, dir string, alreadyHas bool) {
	c := newCoordinatorClaim(dir, alreadyHas)
	eng.SetCoordinatorClaim(c.verify)
}
