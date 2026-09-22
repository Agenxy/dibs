package main

// wakeCovered is the covered half of wakeCoverage: a command here for the
// harness, a thread to name, and the directory on this machine; OR ITS OWN
// MACHINE'S BRIDGE CAN. An agent on another computer is covered when a bridge
// is attached for that host and states its harness: the command is that
// machine's, and so is the check that it exists. The thread is required
// either way.
func wakeCovered(
	a boardAgent, h, hub string, hosts hubHosts,
	have map[string]bool, bridged map[string]map[string]bool,
) bool {
	if !a.Resumable {
		return false
	}
	// The hub's own [wake.exec] reaches only the hub's own agents: the daemon
	// refuses to run it for an agent on another machine (docs/NETWORK.md §5),
	// and doctor used to count it as coverage whenever the harness had a
	// command and the agent's directory happened to exist here too, which on
	// two machines with the same layout reported every remote agent reachable
	// through a route the engine will not take. Found by the pre-release
	// review.
	if have[h] && wakeDirHere(a) && !onAnotherMachine(a, hub, hosts) {
		return true
	}
	// AND A BRIDGE IS A ROUTE FOR ANOTHER MACHINE'S AGENTS ONLY. The
	// engine refuses that route for a local agent outright
	// (hostRouteFor), so counting it here reported a local agent as
	// covered when a bridge attached FOR THIS HOST advertised its
	// harness: doctor said the fleet was reachable and the wake had
	// nowhere to go. Round forty-nine of the pre-release review.
	return !provablyLocal(a, hub, hosts) && a.Agent != nil && bridged[a.Agent.HostID][h]
}

// provablyLocal is positive evidence that the agent is on the hub itself,
// which is the case where the daemon refuses the bridge route outright
// (engine.hostRouteFor). Unknown on either side is NOT provably local: the
// engine treats a hosted agent as remote when it has no identity of its
// own, and doctor has to answer the question the same way the wake will.
// The first version of this asked onAnotherMachine, whose unknown-is-local
// rule is right for the LOCAL command and wrong here; the existing
// coverage test, which passes no hub id at all, said so.
//
// "On the hub" IS THE HUB'S ANSWER, not this side's guess at it. The
// engine reads several ids as itself (engine.SelfHostIDs: the one it
// stamps with, the ledger's node id, and every id it used to answer to),
// and comparing with the board's current id alone calls a row written
// before a Supgang identity was adopted remote. Doctor then reports a
// local agent as covered by a bridge the engine will refuse to use for
// it, which is round forty-nine's finding arriving by a different door.
// The hub states the set at GET /api/hosts; hubHosts.isSelf falls back to
// the old comparison against a daemon too old to say.
func provablyLocal(a boardAgent, hub string, hosts hubHosts) bool {
	return a.Agent != nil && hosts.isSelf(a.Agent.HostID, hub)
}

// onAnotherMachine is positive evidence the agent is not on the hub. Unknown
// on either side is local, which is how every row before the field behaved.
func onAnotherMachine(a boardAgent, hub string, hosts hubHosts) bool {
	if a.Agent == nil || a.Agent.HostID == "" {
		return false
	}
	if len(hosts.self) > 0 {
		return !hosts.self[a.Agent.HostID]
	}
	return hub != "" && a.Agent.HostID != hub
}
