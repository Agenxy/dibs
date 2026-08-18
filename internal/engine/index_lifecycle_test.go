package engine

import (
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

func TestActiveAgentCWDsExcludesTerminalAgents(t *testing.T) {
	e := &Engine{state: &core.State{Agents: map[string]*core.Agent{
		"active": {Status: core.StatusActive, Agent: &core.AgentInfo{CWD: "/work/api"}},
		"stale":  {Status: core.StatusStale, Agent: &core.AgentInfo{CWD: "/work/web"}},
		"closed": {Status: core.StatusClosed, Agent: &core.AgentInfo{CWD: "/work/old"}},
		"empty":  {Status: core.StatusActive},
	}}}

	got := map[string]bool{}
	for _, cwd := range e.activeAgentCWDs() {
		got[cwd] = true
	}
	if !got["/work/api"] || !got["/work/web"] {
		t.Errorf("activeAgentCWDs() = %v, want active and resumable agents", got)
	}
	if got["/work/old"] {
		t.Errorf("activeAgentCWDs() retained a terminal agent: %v", got)
	}
}

func TestRemoveScorerForRepoReleasesOnlySelectedIndex(t *testing.T) {
	e := &Engine{}
	a, b := t.TempDir(), t.TempDir()
	e.SetScorerForRepo(a, fakeScorer{"a"}, MatchConfig{})
	e.SetScorerForRepo(b, fakeScorer{"b"}, MatchConfig{})

	e.RemoveScorerForRepo(a)
	if got, _ := e.scorerFor(a); got != nil {
		t.Fatalf("removed repository still has scorer %q", got.ID())
	}
	if got, _ := e.scorerFor(b); got == nil || got.ID() != "b" {
		t.Fatalf("unremoved repository scorer = %v, want b", got)
	}

	e.RemoveScorerForRepo(b)
	if got := e.IndexedRepos(); len(got) != 0 {
		t.Fatalf("IndexedRepos() after removing both = %v, want empty", got)
	}
}
