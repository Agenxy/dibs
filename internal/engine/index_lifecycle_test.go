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

// The fallback pair is an index and the name of the tree it was mined from,
// used by callers with no agent in hand. Releasing the tree it names has to
// replace BOTH, or Predict answers questions about one repository out of
// another's history, which is the confident nonsense scorerFor exists to stop.
func TestReleasingTheFallbackRepoReplacesBothHalves(t *testing.T) {
	e := &Engine{}
	a, b := t.TempDir(), t.TempDir()
	e.SetScorerForRepo(a, fakeScorer{"a"}, MatchConfig{})
	e.SetScorerForRepo(b, fakeScorer{"b"}, MatchConfig{})
	// SetScorerForRepo leaves the pair naming whichever was published last.
	if e.matchRepo() != b {
		t.Fatalf("setup: the fallback names %q, want %q", e.matchRepo(), b)
	}

	e.RemoveScorerForRepo(b)

	repo := e.matchRepo()
	if repo != a {
		t.Fatalf("after releasing the tree the fallback named, it names %q, want %q", repo, a)
	}
	scorer, cfg := e.scorerAndCfg()
	if scorer == nil {
		t.Fatal("the fallback index is nil while a tree is still indexed")
	}
	if got := scorer.ID(); got != "a" {
		t.Errorf("the fallback index is %q while its config names %q: one repository's "+
			"history is answering for another", got, cfg.Repo)
	}
	if cfg.Repo != repo {
		t.Errorf("the config names %q and the accessor says %q", cfg.Repo, repo)
	}

	// And releasing the last one clears both rather than leaving a dangling name.
	e.RemoveScorerForRepo(a)
	if s, c := e.scorerAndCfg(); s != nil || c.Repo != "" {
		t.Errorf("after releasing every index the fallback is (%v, %q), want (nil, \"\")", s, c.Repo)
	}
}

// Releasing a tree that was never indexed must not disturb the fallback.
func TestReleasingAnUnheldRepoChangesNothing(t *testing.T) {
	e := &Engine{}
	a := t.TempDir()
	e.SetScorerForRepo(a, fakeScorer{"a"}, MatchConfig{})

	e.RemoveScorerForRepo(t.TempDir())

	if e.matchRepo() != a {
		t.Errorf("releasing an unheld tree moved the fallback to %q", e.matchRepo())
	}
	if s, _ := e.scorerAndCfg(); s == nil || s.ID() != "a" {
		t.Errorf("releasing an unheld tree replaced the fallback index")
	}
}
