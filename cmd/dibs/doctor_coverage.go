package main

// wakeCovered is the covered half of wakeCoverage: a command here for the
// harness, a thread to name, and the directory on this machine; OR ITS OWN
// MACHINE'S BRIDGE CAN. An agent on another computer is covered when a bridge
// is attached for that host and states its harness: the command is that
// machine's, and so is the check that it exists. The thread is required
// either way.
func wakeCovered(a boardAgent, h, hub string, have map[string]bool, bridged map[string]map[string]bool) bool {
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
	if have[h] && wakeDirHere(a) && !onAnotherMachine(a, hub) {
		return true
	}
	return a.Agent != nil && bridged[a.Agent.HostID][h]
}

// onAnotherMachine is positive evidence the agent is not on the hub. Unknown
// on either side is local, which is how every row before the field behaved.
func onAnotherMachine(a boardAgent, hub string) bool {
	return a.Agent != nil && a.Agent.HostID != "" && hub != "" && a.Agent.HostID != hub
}
