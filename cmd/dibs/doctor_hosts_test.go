package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/supgang"
)

// A remote agent is named by the machine it is on, the way the fleet names
// it: its host id looked up among Supgang's members. The hostname its bridge
// reported stands in when Supgang does not know the id.
func TestDoctorNamesTheMachineARemoteAgentIsOnThroughSupgang(t *testing.T) {
	supgangNamesOnce.Do(func() {})
	old := supgangNames
	supgangNames = map[string]string{"a8a37e32" + strings.Repeat("0", 56): "MacSolis"}
	t.Cleanup(func() { supgangNames = old })

	known := agentRow("far", "persistent", "codex")
	known.Host = "some-hostname"
	known.Agent.HostID = "a8a37e32" + strings.Repeat("0", 56)
	unknown := agentRow("stranger", "persistent", "codex")
	unknown.Host = "laptop.local"
	unknown.Agent.HostID = strings.Repeat("f", 64)
	nowhere := agentRow("nowhere", "persistent", "codex")
	if hostLabel(known) != "MacSolis" || hostLabel(unknown) != "laptop.local" || hostLabel(nowhere) != "" {
		t.Errorf("labels = %q %q %q", hostLabel(known), hostLabel(unknown), hostLabel(nowhere))
	}
	// And the coverage report carries the name, so "no route" says where.
	for _, r := range []*boardAgent{&known, &unknown} {
		r.Agent.CWD = t.TempDir() + "/gone"
	}
	_, missing := wakeCoverage(boardOf(known, unknown), map[string]bool{"codex": true}, nil)
	if missing["codex (on MacSolis)"] != 1 || missing["codex (on laptop.local)"] != 1 {
		t.Errorf("missing = %v, want each remote agent bucketed by its machine's name", missing)
	}
	// A bridge attached for MacSolis that can start codex covers the agent
	// there; one that can start only claude does not; the thread is still
	// required.
	bridged := map[string]map[string]bool{known.Agent.HostID: {"codex": true}}
	covered, missing := wakeCoverage(boardOf(known, unknown), map[string]bool{"codex": true}, bridged)
	if covered != 1 || missing["codex (on MacSolis)"] != 0 || missing["codex (on laptop.local)"] != 1 {
		t.Errorf("with MacSolis bridged: covered=%d missing=%v", covered, missing)
	}
	if c, _ := wakeCoverage(boardOf(known), nil, map[string]map[string]bool{known.Agent.HostID: {"claude code": true}}); c != 0 {
		t.Error("a bridge that cannot start the agent's harness counted as coverage")
	}
	known.Resumable = false
	if c, _ := wakeCoverage(boardOf(known), nil, bridged); c != 0 {
		t.Error("a bridged agent with no thread counted as coverage")
	}
}

// On a machine joined to a hub elsewhere, doctor says what would let the hub
// wake the agents here: the entries in this machine's dibs.toml, and a host
// bridge attached for this machine, each named when it is missing.
//
// AND COMPARES WHAT THE BRIDGE ADVERTISED with what the file has and what
// the agents here need. It counted commands and asked whether a bridge was
// attached, so a bridge started before [wake.exec.claude] was added was
// reported as reaching claude, and an agent here on a harness with no entry
// at all went unmentioned. Round twenty-five of the pre-release review.
func TestDoctorOnAJoinedMachineNamesItsOwnWakeRoute(t *testing.T) {
	both := joinedWake{
		configured: []string{"codex", "claude"}, advertised: []string{"codex", "claude"},
		attached: true, agents: map[string]int{"codex": 1},
	}
	okMsg, warnMsg, _ := joinedWakeAdvice(both, "abc", "/d")
	if okMsg == "" || warnMsg != "" || !strings.Contains(okMsg, "codex, claude") {
		t.Errorf("attached, advertising every route: ok=%q warn=%q", okMsg, warnMsg)
	}
	detached := both
	detached.attached = false
	okMsg, warnMsg, fix := joinedWakeAdvice(detached, "abc", "/d")
	if okMsg != "" || !strings.Contains(warnMsg, "no host bridge is attached") || !strings.Contains(fix, "dibs host-bridge") {
		t.Errorf("routes, not attached: ok=%q warn=%q fix=%q", okMsg, warnMsg, fix)
	}
	okMsg, warnMsg, fix = joinedWakeAdvice(joinedWake{attached: true}, "abc", "/d")
	if okMsg != "" || !strings.Contains(warnMsg, "no [wake.exec]") || !strings.Contains(fix, "/d/dibs.toml") {
		t.Errorf("no routes: ok=%q warn=%q fix=%q", okMsg, warnMsg, fix)
	}
	// The bridge started before claude was added: the file has two routes
	// and the hub can use one.
	stale := both
	stale.advertised = []string{"codex"}
	okMsg, warnMsg, fix = joinedWakeAdvice(stale, "abc", "/d")
	if okMsg != "" || !strings.Contains(warnMsg, "[wake.exec.claude]") || !strings.Contains(fix, "restart") {
		t.Errorf("a route the bridge did not advertise was reported as reaching agents: ok=%q warn=%q fix=%q",
			okMsg, warnMsg, fix)
	}
	// An agent here on a harness nothing can start.
	gap := both
	gap.agents = map[string]int{"codex": 1, "gemini": 2}
	okMsg, warnMsg, fix = joinedWakeAdvice(gap, "abc", "/d")
	if okMsg != "" || !strings.Contains(warnMsg, "gemini (2)") || !strings.Contains(fix, "[wake.exec.<harness>]") {
		t.Errorf("agents on an unconfigured harness went unmentioned: ok=%q warn=%q fix=%q", okMsg, warnMsg, fix)
	}
}

// A hub's doctor compares what Supgang says this computer's Dibs serves
// with what it serves: nothing advertised, the wrong port, a stale key, each
// with the verb that mends it; agreement is a tick; an older Supgang that
// carries no advertisements at all is left alone, because a hub that did not
// advertise and a Supgang that cannot are different things.
func TestDoctorSaysWhetherSupgangAdvertisesThisHub(t *testing.T) {
	pin := strings.Repeat("ab", 32)
	self := supgang.Peer{ServicesKnown: true, Services: []supgang.Service{{Name: "dibs", Port: 4777, KeyPin: pin}}}
	if msg, _ := hubAdvertisementDrift(self, "4777", pin); msg != "" {
		t.Errorf("an advertised hub was reported: %q", msg)
	}
	if msg, _ := hubAdvertisementDrift(supgang.Peer{}, "4777", pin); msg != "" {
		t.Errorf("a Supgang without advertisements was told to advertise: %q", msg)
	}
	cases := map[string]supgang.Peer{
		"by hand":        {ServicesKnown: true},
		"wrong port":     {ServicesKnown: true, Services: []supgang.Service{{Name: "dibs", Port: 4790, KeyPin: pin}}},
		"as an impostor": {ServicesKnown: true, Services: []supgang.Service{{Name: "dibs", Port: 4777, KeyPin: strings.Repeat("cd", 32)}}},
	}
	for want, peer := range cases {
		msg, fix := hubAdvertisementDrift(peer, "4777", pin)
		if !strings.Contains(msg, want) || !strings.Contains(fix, "supgang advertise dibs 4777 --key-pin "+pin) {
			t.Errorf("%s: msg=%q fix=%q", want, msg, fix)
		}
	}
}

// A hub whose own table has no wake commands still serves agents on other
// machines, and their route is their machines' bridges: doctor says so
// whichever way [wake] sockets is set, rather than reporting the hub's
// empty table as the only route there is.
func TestDoctorReportsBridgeCoverageWithoutALocalWakeTable(t *testing.T) {
	supgangNamesOnce.Do(func() {})
	far := agentRow("far", "persistent", "codex")
	far.Agent.HostID = strings.Repeat("c", 64)
	for _, sockets := range []string{"[wake]\nsockets = false\n", ""} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "node_id"), []byte("hub-1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte(sockets), 0o600); err != nil {
			t.Fatal(err)
		}
		board := &boardView{Node: "hub-1", HostID: strings.Repeat("f", 64), Agents: []boardAgent{far}}
		var oks, warns []string
		hosts := []engine.HostBridgeInfo{{Host: far.Agent.HostID, Harnesses: []string{"codex"}}}
		checkWakeRoutes(dir, board, hosts, func(m string) { oks = append(oks, m) }, func(w, _ string) { warns = append(warns, w) })
		if !containsLine(oks, "on other machines have a wake route") || !containsLine(oks, "host bridge(s) attached") {
			t.Errorf("sockets=%q: an attached bridge covering the remote agent was not reported: oks=%q", sockets, oks)
		}
		if containsLine(warns, "on other machines have no wake route") {
			t.Errorf("sockets=%q: a covered remote agent was reported as unreachable: %q", sockets, warns)
		}
		oks, warns = nil, nil
		checkWakeRoutes(dir, board, nil, func(m string) { oks = append(oks, m) }, func(w, _ string) { warns = append(warns, w) })
		if !containsLine(warns, "1 agent(s) on other machines have no wake route") {
			t.Errorf("sockets=%q: with no bridge attached the remote agent was not reported: warns=%q", sockets, warns)
		}
	}
}

func containsLine(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// A daemon started before this machine joined its hive still stamps its
// agents with the ledger's id; doctor says so and names the restart, since
// nothing else corrects it.
func TestDoctorSaysWhenTheDaemonPredatesSupgang(t *testing.T) {
	node := "fed08b44" + strings.Repeat("0", 56)
	if d := hostIdentityDrift(node, node); d != "" {
		t.Errorf("agreeing identities reported drift: %q", d)
	}
	if d := hostIdentityDrift("1aa62a8d", ""); d != "" {
		t.Errorf("no Supgang reported drift: %q", d)
	}
	if d := hostIdentityDrift("", node); d != "" {
		t.Errorf("a board with no host id reported drift: %q", d)
	}
	if d := hostIdentityDrift("1aa62a8d", "abc"); !strings.Contains(d, "abc") {
		t.Errorf("a short Supgang id was not reported safely: %q", d)
	}
	d := hostIdentityDrift("1aa62a8d", node)
	if !strings.Contains(d, "fed08b44") || !strings.Contains(d, "1aa62a8d") || !strings.Contains(d, "started before") {
		t.Errorf("drift = %q, want both identities and the cause", d)
	}
}

// The hub's own [wake.exec] is not a route to an agent on another machine,
// however familiar that agent's directory looks from here.
//
// wakeCovered answered yes whenever the harness had a local command and the
// agent's cwd existed on the hub, without asking where the agent was, so two
// machines with the same layout and no bridge attached read as fully
// covered while the engine refuses to run the hub's command for them.
// Found by the pre-release review.
func TestDoctorDoesNotCountALocalCommandAsCoverageForARemoteAgent(t *testing.T) {
	supgangNamesOnce.Do(func() {})
	dir := t.TempDir() // exists here, as the remote agent's cwd "does" too
	far := agentRow("far", "persistent", "codex")
	far.Agent.HostID = strings.Repeat("c", 64)
	far.Agent.CWD = dir
	hub := strings.Repeat("f", 64)
	have := map[string]bool{"codex": true}
	if wakeCovered(far, "codex", hub, have, nil) {
		t.Fatal("a remote agent was reported covered by the hub's own command, which the " +
			"daemon will never run for it")
	}
	// The same agent, with its host's bridge attached, is covered through it.
	bridged := map[string]map[string]bool{far.Agent.HostID: {"codex": true}}
	if !wakeCovered(far, "codex", hub, have, bridged) {
		t.Fatal("a remote agent whose host bridge claims its harness was not counted")
	}
	// And a local agent is covered by the local command, as before.
	near := agentRow("near", "persistent", "codex")
	near.Agent.HostID = hub
	near.Agent.CWD = dir
	if !wakeCovered(near, "codex", hub, have, nil) {
		t.Fatal("a local agent with a local command was not counted")
	}
}
