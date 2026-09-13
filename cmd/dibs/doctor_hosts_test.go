package main

import (
	"strings"
	"testing"
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
	_, missing := wakeCoverage(boardOf(known, unknown), map[string]bool{"codex": true})
	if missing["codex (on MacSolis)"] != 1 || missing["codex (on laptop.local)"] != 1 {
		t.Errorf("missing = %v, want each remote agent bucketed by its machine's name", missing)
	}
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
