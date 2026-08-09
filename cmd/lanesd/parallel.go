package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/agenxy/lanes/internal/paths"
)

// Guard against a second daemon quietly partitioning the fleet.
//
// The flock on <dir>/lock stops two daemons sharing a data directory. It does
// nothing about the case that actually hurts: two daemons on DIFFERENT
// directories and different ports, both healthy, each with its own board. Some
// agents register with one and some with the other, every call succeeds, both
// boards look correct, and no agent can see half the fleet.
//
// For this product specifically that is the worst failure available. Lanes
// exists so two agents doing the same job find each other; a partition removes
// exactly that guarantee while leaving every surface looking fine. It is the
// silent-wrong-answer shape the rest of the design works hard to avoid, so it
// should not be the default.
//
// It is still a legitimate thing to want. SECURITY.md's advice for agents you do
// not trust is to run them against a second daemon, because the trust boundary
// is the machine. So this is a prompt, not a prohibition: refuse by default,
// name the daemon already running, and say the one word that allows it.
//
// The registry mechanics — atomic claim, lock-based liveness, canonical
// directory identity — live in internal/paths, which explains why each is
// necessary. What lives here is the policy: who is allowed to start.

// parallelAllowed reports whether the operator has said a second daemon is
// intended. The environment variable exists because test suites legitimately
// run many daemons at once and setting it per suite is one line, where threading
// a flag through two dozen spawn sites is two dozen chances to forget.
func parallelAllowed(flagSet bool) bool {
	return flagSet || os.Getenv("LANES_ALLOW_PARALLEL") != ""
}

// refusalText turns the registry's answer into something a person can act on.
func refusalText(others []paths.Daemon) error {
	var b strings.Builder
	b.WriteString("another lanesd is already running on this machine:\n")
	for _, o := range others {
		if o.Unknown {
			// Held, but we could not read it. Say so rather than printing an
			// empty row that looks like a bug.
			b.WriteString("  (a daemon whose registration could not be read)\n")
			continue
		}
		fmt.Fprintf(&b, "  pid %d  %s  (data %s)\n", o.PID, o.Addr, o.Dir)
	}
	b.WriteString(
		"\nTwo daemons means two boards. Agents pointed at different ones cannot see\n" +
			"each other, every call still succeeds, and both boards look correct — which is\n" +
			"the exact failure Lanes exists to prevent, made invisible.\n\n" +
			"If you meant to isolate agents you do not trust (SECURITY.md), that is a real\n" +
			"reason and this is how you say so:\n" +
			"  lanesd -allow-parallel ...      or   LANES_ALLOW_PARALLEL=1 lanesd ...\n\n" +
			"Otherwise, point your agents at the daemon above, or stop it first.")
	return errors.New(b.String())
}

// claimHostSlot establishes that this process is the daemon for this data
// directory AND, unless told otherwise, for this machine.
//
// Extracted from run() rather than suppressed: the complexity ceiling caught it
// growing, and a startup sequence is exactly the place where "one more check
// inline" accumulates until nobody can see the order things happen in.
func claimHostSlot(addr, dir string, allowed bool) (func(), error) {
	// 1. This data directory. flock held for the process lifetime; two daemons
	//    sharing a directory would interleave writes into one ledger.
	lockPath := filepath.Join(dir, "lock")
	// #nosec G304 -- a path inside the daemon's own data directory, or one the
	// operator pointed the CLI at. Same-user access only; refusing it would mean
	// refusing to run.
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return func() {}, err
	}
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lockFile.Close()
		return func() {}, fmt.Errorf("another lanesd already runs on %s (flock %s): %w",
			dir, lockPath, err)
	}
	closeLock := func() { _ = lockFile.Close() }

	// 2. This machine. Reading the registry and claiming a slot happen under one
	//    host-wide lock, so two daemons starting together cannot both conclude
	//    they are alone — which a check-then-register pair allowed.
	unregister, err := paths.Claim(paths.Daemon{PID: os.Getpid(), Addr: addr, Dir: dir}, allowed)
	if err != nil {
		closeLock()
		// The policy is a plain bool, so nothing of ours runs while the registry
		// lock is held — a callback there would deadlock the moment it wanted to
		// consult the registry, which is the obvious thing for a policy to want.
		// The refusal is turned into prose out here instead.
		var strangers *paths.Strangers
		if errors.As(err, &strangers) {
			return func() {}, refusalText(strangers.Others)
		}
		// Fails CLOSED, and that is a correction. Treating an unwritable registry
		// as harmless meant the one mechanism preventing a partition could vanish
		// silently, exactly when its state could not be trusted. If Lanes cannot
		// establish that it is alone, it says so rather than assuming.
		return func() {}, fmt.Errorf(
			"cannot verify this is the only lanesd on this machine: %w\n\n"+
				"The host registry under %s is unreadable or unwritable. Two daemons would\n"+
				"split the fleet silently, so this is not something to guess at. Fix that\n"+
				"directory, or pass -allow-parallel if you accept the risk",
			err, paths.RunRegistryDir())
	}
	return func() { unregister(); closeLock() }, nil
}
