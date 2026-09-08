package main

import (
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/boardconfig"
)

// With a wake command for one harness and an agent of another holding a
// resumable thread, the report assumed that having no built-in block to
// paste for that harness meant it already had a command, and told the
// operator to register through its plugin, which cannot supply
// configuration. A bare missing key is a harness with no command.
func TestDoctorNamesTheHarnessWithNoWakeCommand(t *testing.T) {
	exec := map[string]boardconfig.WakeExec{"codex": {}}
	b := &boardView{Agents: []boardAgent{
		{ID: "oc", Kind: "persistent", Resumable: true, Agent: &struct {
			Harness string `json:"harness,omitempty"`
			CWD     string `json:"cwd,omitempty"`
			Surface string `json:"surface,omitempty"`
		}{Harness: "OpenCode"}},
	}}
	var fix string
	reportWakeCoverage(exec, b, t.TempDir(),
		func(string) { t.Fatal("an uncovered agent was reported as covered") },
		func(_, f string) { fix = f })
	if fix == "" {
		t.Fatal("setup: nothing was reported")
	}
	if strings.Contains(fix, "registering through its harness's Dibs plugin") {
		t.Fatalf("the report tells OpenCode to re-register, which cannot supply the command it lacks:\n%s", fix)
	}
	if !strings.Contains(fix, `[wake.exec."opencode"]`) {
		t.Fatalf("the report does not hand the operator a block for the harness with no command:\n%s", fix)
	}
	// And a harness that HAS a command and lacks a thread still gets the
	// no-thread advice, with nothing to paste.
	fix = ""
	b.Agents[0].Agent.Harness = "Codex"
	b.Agents[0].Resumable = false
	reportWakeCoverage(exec, b, t.TempDir(), func(string) { t.Fatal("reported as covered") }, func(_, f string) { fix = f })
	if !strings.Contains(fix, "no thread") {
		t.Fatalf("a harness with a command and no thread is not told so:\n%s", fix)
	}
}
