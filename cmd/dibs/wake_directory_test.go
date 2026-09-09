package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/boardconfig"
)

// boardAgentInfo builds the anonymous struct boardAgent declares inline, so the
// tests below read as what they are testing rather than as a type literal.
func boardAgentInfo(harness, cwd string) *struct {
	Harness string `json:"harness,omitempty"`
	CWD     string `json:"cwd,omitempty"`
	Surface string `json:"surface,omitempty"`
} {
	return &struct {
		Harness string `json:"harness,omitempty"`
		CWD     string `json:"cwd,omitempty"`
		Surface string `json:"surface,omitempty"`
	}{Harness: harness, CWD: cwd}
}

// A WAKE COMMAND RUNS ON THIS MACHINE, IN THE AGENT'S OWN DIRECTORY, and
// coverage never asked whether that directory is here.
//
// runWakeForOut sets cmd.Dir to the agent's recorded cwd and falls back to the
// daemon's when it is gone, warning as it does. That fallback is a wake that
// will usually fail: `codex exec resume` refuses outright outside a trusted
// directory, exit 1, which is this repository's own daemon log three times over
// after launchd started the daemon in "/". So an agent with a command, a
// resumable thread and a directory that is not on this machine is not covered,
// and was counted as covered.
//
// Two ways to arrive there, and they will not stay equally rare. A worktree
// that has been removed is the local one, and it is ordinary here: AGENTS.md
// instructs agents to `git worktree add` to check a regression test and then
// take it away again. The other is an agent on a DIFFERENT COMPUTER, where the
// path exists perfectly well and not here, and the hub cannot run anything for
// it at all. That is the measurement behind docs/NETWORK.md §5: the hub decides
// THAT an agent should be woken, and only the agent's own machine can decide
// how.
//
// The same failure shape as the count-versus-coverage rewrite that produced
// this function: configuration that exists, reported as configuration that
// works.
func TestDoctorSaysAnAgentsDirectoryIsNotOnThisMachine(t *testing.T) {
	exec := map[string]boardconfig.WakeExec{"codex": {}}
	gone := filepath.Join(t.TempDir(), "removed-worktree")
	b := &boardView{Agents: []boardAgent{
		{ID: "far", Kind: "persistent", Resumable: true, Agent: boardAgentInfo("Codex", gone)},
	}}

	var head, fix string
	reportWakeCoverage(exec, b, t.TempDir(),
		func(string) {
			t.Fatal("an agent whose working directory is not on this machine was " +
				"reported as covered: the wake would run in the daemon's own " +
				"directory, where the documented command refuses to start")
		},
		func(h, f string) { head, fix = h, f })

	if head == "" {
		t.Fatal("nothing was reported at all")
	}
	if !strings.Contains(head, "directory") {
		t.Errorf("the report does not name what is actually missing, so an operator "+
			"reads it as a configuration problem and adds a block they already have:\n%s", head)
	}
	if strings.Contains(fix, "[wake.exec.") {
		t.Errorf("the report hands over a block to paste for a harness that already "+
			"has one. What is wrong is the directory, and advice that does not "+
			"apply is how an operator learns to skim the rest:\n%s", fix)
	}
	if !strings.Contains(fix, gone) {
		t.Errorf("the fix does not name the directory it is about:\n%s", fix)
	}
}

// And an agent whose directory IS here is still covered, so the check cannot
// pass by reporting everything as broken.
func TestDoctorStillCoversAnAgentWhoseDirectoryIsHere(t *testing.T) {
	exec := map[string]boardconfig.WakeExec{"codex": {}}
	here := t.TempDir()
	b := &boardView{Agents: []boardAgent{
		{ID: "near", Kind: "persistent", Resumable: true, Agent: boardAgentInfo("Codex", here)},
	}}

	var said string
	reportWakeCoverage(exec, b, t.TempDir(),
		func(s string) { said = s },
		func(h, _ string) { t.Fatalf("an agent that can be woken was reported as uncovered: %s", h) })
	if !strings.Contains(said, "covering all") {
		t.Errorf("the healthy case is not reported as covered: %q", said)
	}
}

// An agent that recorded NO directory is not the same as one whose directory is
// missing, and must not be reported as if it were.
//
// The wake runs where the daemon does, deliberately, and the code says so:
// "a wake that might work beats one that certainly does not". Nothing is broken
// and there is nothing to fix, so saying something here would be a warning that
// is always present, which is the habituation this report exists to avoid.
func TestDoctorDoesNotWarnAboutAnAgentThatRecordedNoDirectory(t *testing.T) {
	exec := map[string]boardconfig.WakeExec{"codex": {}}
	b := &boardView{Agents: []boardAgent{
		{ID: "nodir", Kind: "persistent", Resumable: true, Agent: boardAgentInfo("Codex", "")},
	}}

	reportWakeCoverage(exec, b, t.TempDir(),
		func(string) {},
		func(h, _ string) {
			t.Fatalf("an agent that never recorded a directory was reported as having "+
				"a broken one: %s", h)
		})
}
