package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/agenxy/dibs/internal/boardconfig"
	"github.com/agenxy/dibs/internal/engine"
)

// checkJoinedWakeRoutes is the wake check on a machine whose board is served
// elsewhere. The HUB's wake configuration is on the hub, and this used to
// say only that; but this machine's own agents are woken by this machine's
// [wake.exec], run by `dibs host-bridge` (docs/NETWORK.md §5), and that is
// configured here and can be checked here: the entries exist, and a bridge
// is attached for this host.
func checkJoinedWakeRoutes(dir string, b *boardView, hosts hubHosts, ok reportFn, warn fixFn) {
	var j joinedWake
	if cfg, err := boardconfig.Load(dir); err == nil {
		for h := range cfg.Wake.Exec {
			j.configured = append(j.configured, strings.ToLower(h))
		}
	}
	for _, h := range hosts.bridges {
		if h.Host == hostID() {
			j.attached = true
			for _, name := range h.Harnesses {
				j.advertised = append(j.advertised, strings.ToLower(name))
			}
		}
	}
	// The agents this machine's bridge is FOR: persistent, wakeable, and
	// recorded as being here. One with no resumable thread is counted
	// SEPARATELY rather than skipped: the wake is refused for it whatever
	// the table says (wakeRoute), and skipping it let this report "covers
	// every wakeable agent recorded here" about a machine whose only agent
	// could not be woken at all. Round twenty-seven of the pre-release
	// review; it was round twenty-five's own filter.
	j.agents = map[string]int{}
	for _, a := range b.Agents {
		if a.Kind != "persistent" || !wakeable(a) || a.Agent == nil || a.Agent.HostID != hostID() {
			continue
		}
		if !a.Resumable {
			j.threadless++
			continue
		}
		h := strings.ToLower(a.Agent.Harness)
		if h == "" {
			h = "(no harness recorded)"
		}
		j.agents[h]++
	}
	okMsg, warnMsg, fix := joinedWakeAdvice(j, b.Node, dir)
	if okMsg != "" {
		ok(okMsg)
	}
	if warnMsg != "" {
		warn(warnMsg, fix)
	}
}

// joinedWake is what doctor knows about this machine's wake route on a
// board served elsewhere: the harnesses its dibs.toml has commands for, the
// harnesses the bridge attached for this machine ADVERTISED when it
// started, and the agents here that need one, per harness.
type joinedWake struct {
	configured []string
	advertised []string
	attached   bool
	agents     map[string]int
	// threadless counts the agents here that no route can wake whatever
	// this machine configures: they have never supplied a harness thread
	// for a resume command to name.
	threadless int
}

// joinedWakeAdvice is the decision checkJoinedWakeRoutes reports: what is
// true of this machine's routes, and what would make the hub able to wake
// its agents. The hub's own coverage is still the hub's to report, and the
// advice says where. Split from the fetches so it can be tested without a
// daemon: the fetches are the caller's, because a check that called the
// network from inside was once measured against the developer's live board.
//
// THE BRIDGE'S WORD, NOT THE FILE'S. This counted commands in the file and
// asked whether a bridge was attached, and said "2 wake command(s) reach
// this machine's agents" for a bridge that had advertised only one of them:
// the bridge loads its routes when it starts, so an entry added afterwards
// reaches nobody until it is restarted; and an agent here on a harness with
// no entry at all was not mentioned, because nothing looked at the agents.
// Round twenty-five of the pre-release review.
func joinedWakeAdvice(j joinedWake, node, dir string) (okMsg, warnMsg, fix string) {
	toml := filepath.Join(dir, "dibs.toml")
	restart := "run `dibs host-bridge` with the same DIBS_ADDR and DIBS_DIR, and keep it running; the " +
		"hub's own coverage is reported by `dibs doctor` on the machine that runs the daemon"
	switch {
	case len(j.configured) == 0:
		return "", fmt.Sprintf("this board is served by another daemon (node %s), and %s has no [wake.exec] "+
				"entry, so agents on this machine cannot be woken: the hub's own commands run on the hub", node, toml),
			"add a [wake.exec.<harness>] block to " + toml + " (docs/CONFIGURATION.md) and run `dibs host-bridge`; " +
				"the hub's own coverage is reported by `dibs doctor` on the machine that runs the daemon"
	case !j.attached:
		return "", fmt.Sprintf("this board is served by another daemon (node %s), and no host bridge is "+
			"attached for this machine, so the %d wake command(s) in %s never run: the hub cannot "+
			"wake an agent here", node, len(j.configured), toml), restart
	}
	advertised := map[string]bool{}
	for _, h := range j.advertised {
		advertised[h] = true
	}
	var stale, uncovered []string
	for _, h := range j.configured {
		if !advertised[h] {
			stale = append(stale, h)
		}
	}
	for h, n := range j.agents {
		if !advertised[h] {
			uncovered = append(uncovered, fmt.Sprintf("%s (%d)", h, n))
		}
	}
	sort.Strings(stale)
	sort.Strings(uncovered)
	switch {
	case len(stale) > 0:
		return "", fmt.Sprintf("this board is served by another daemon (node %s); the host bridge attached "+
				"for this machine started before %s gained [wake.exec.%s], so the hub cannot use those "+
				"routes until it is restarted", node, toml, strings.Join(stale, "], [wake.exec.")),
			"restart `dibs host-bridge` on this machine; it reads its routes when it starts"
	case len(uncovered) > 0:
		return "", fmt.Sprintf("this board is served by another daemon (node %s); agents on this machine "+
				"run harnesses the host bridge attached for it cannot start: %s", node, strings.Join(uncovered, ", ")),
			"add a [wake.exec.<harness>] block for each to " + toml + " and restart `dibs host-bridge`"
	case j.threadless > 0:
		return "", fmt.Sprintf("this board is served by another daemon (node %s); the host bridge attached "+
				"for this machine can start %s, and %d agent(s) here have never supplied a harness thread "+
				"for a command to resume, so no route can wake them",
				node, strings.Join(j.advertised, ", "), j.threadless),
			"those agents reattach with their nonce inside a session their harness can resume (`resume`), " +
				"which is what records the thread; until then their mail waits for their next check_in"
	}
	return fmt.Sprintf("this board is served by another daemon (node %s); the host bridge attached for "+
		"this machine can start %s, which covers every wakeable agent recorded here",
		node, strings.Join(j.advertised, ", ")), "", ""
}

// hubHosts is what the hub reports through GET /api/hosts: the bridges
// attached now with the harnesses each can start, AND the host ids the hub
// reads as itself.
//
// The second is not decoration. A bridge is a route for another machine's
// agents and never for a local one, and the hub's own [wake.exec] is the
// reverse, so every coverage answer here turns on "is this agent on the
// hub". Doctor used to decide that by comparing with the board's host id,
// which is the hub's CURRENT id and not the whole answer: rows written
// before the machine adopted a Supgang identity carry the ledger's node
// id, and rows older still carry an id kept as an alias. Both read as
// remote, so a local agent was reported covered by a bridge the engine
// refuses to use for it. The hub knows the set; it states it.
type hubHosts struct {
	bridges []engine.HostBridgeInfo
	// self is empty against a daemon too old to say, and every caller
	// falls back to the board's host id then rather than calling
	// everything remote.
	self map[string]bool
}

// attachedHosts asks the hub. Zero value when it cannot say. Fetched by
// doctor itself and passed down, never from inside a check.
func attachedHosts() hubHosts {
	var out struct {
		Hosts []engine.HostBridgeInfo `json:"hosts"`
		Self  []string                `json:"self"`
	}
	if err := get("/api/hosts", &out); err != nil {
		return hubHosts{}
	}
	h := hubHosts{bridges: out.Hosts}
	for _, id := range out.Self {
		if id == "" {
			continue
		}
		if h.self == nil {
			h.self = map[string]bool{}
		}
		h.self[id] = true
	}
	return h
}

// isSelf reports an agent's host as one the hub reads as its own. With no
// answer from the hub it compares with the board's id, which is what this
// did before the hub stated the set.
func (h hubHosts) isSelf(hostID, boardHostID string) bool {
	if hostID == "" {
		return false
	}
	if len(h.self) > 0 {
		return h.self[hostID]
	}
	return boardHostID != "" && hostID == boardHostID
}
