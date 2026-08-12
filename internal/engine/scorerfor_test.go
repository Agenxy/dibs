package engine

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/agenxy/dibs/internal/overlap"
)

// fakeScorer is identity only: these tests are about WHICH index is chosen.
type fakeScorer struct{ id string }

func (f fakeScorer) ID() string      { return f.id }
func (f fakeScorer) Version() string { return "test" }
func (f fakeScorer) Predict(context.Context, string, int) (overlap.Prediction, error) {
	return overlap.Prediction{ScorerID: f.id}, nil
}

// An agent must be scored by the tree it is working in.
//
// Matching used to hold one index for the whole daemon, chosen by a flag, so a
// fleet spread over three projects had two of them scored against a fourth
// one's history. A co-change model only means anything inside the history it
// was mined from: asked about another project's sentence it does not decline,
// it answers confidently and wrongly, which is worse than no matching at all.
func TestEachAgentIsScoredByItsOwnRepository(t *testing.T) {
	e := &Engine{}
	api := t.TempDir()
	web := t.TempDir()
	e.SetScorerForRepo(api, fakeScorer{"api"}, MatchConfig{})
	e.SetScorerForRepo(web, fakeScorer{"web"}, MatchConfig{})

	for _, tc := range []struct{ cwd, want string }{
		{api, "api"},
		{filepath.Join(api, "internal", "session"), "api"},
		{web, "web"},
		{filepath.Join(web, "src"), "web"},
	} {
		got, cfg := e.scorerFor(tc.cwd)
		if got == nil {
			t.Errorf("cwd %s got no scorer", tc.cwd)
			continue
		}
		if got.ID() != tc.want {
			t.Errorf("cwd %s scored by %q, want %q: an index answers confidently "+
				"about a history it was not built from", tc.cwd, got.ID(), tc.want)
		}
		if cfg.Repo == "" {
			t.Errorf("cwd %s: config carries no repo, so nothing downstream can say "+
				"which tree the suggestion came from", tc.cwd)
		}
	}
}

// A nested checkout is scored by its own index, not the parent it sits inside.
func TestTheInnermostRepositoryWins(t *testing.T) {
	e := &Engine{}
	outer := t.TempDir()
	inner := filepath.Join(outer, "vendor", "thing")
	e.SetScorerForRepo(outer, fakeScorer{"outer"}, MatchConfig{})
	e.SetScorerForRepo(inner, fakeScorer{"inner"}, MatchConfig{})

	got, _ := e.scorerFor(filepath.Join(inner, "src"))
	if got == nil || got.ID() != "inner" {
		t.Errorf("nested checkout scored by %v, want the inner index", got)
	}
}

// An agent outside every indexed tree gets no scorer, and that is correct: it
// still receives the shared-refs and shared-dirs signals, which are computed in
// the pure core and need no index. Handing it someone else's index would be a
// confident wrong answer in place of an honest absent one.
func TestAnAgentOutsideEveryIndexedTreeIsNotScored(t *testing.T) {
	e := &Engine{}
	e.SetScorerForRepo(t.TempDir(), fakeScorer{"api"}, MatchConfig{})

	if got, _ := e.scorerFor(t.TempDir()); got != nil {
		t.Errorf("an agent in an unindexed tree was scored by %q", got.ID())
	}
}

// The repository is learned from the agents, so the callback must actually fire
// on registration. Without it nothing is ever indexed and matching is off for
// everyone, which is the state this whole change exists to end.
func TestRegisteringReportsItsRepositoryOnce(t *testing.T) {
	e := &Engine{}
	var seen []string
	e.OnRepoSeen(func(repo string) { seen = append(seen, repo) })

	e.noteRepoOf("/work/api")
	e.noteRepoOf("/work/api") // same tree again: already known
	e.noteRepoOf("/work/web")
	e.noteRepoOf("") // an agent that reported no cwd

	if len(seen) != 2 || seen[0] != "/work/api" || seen[1] != "/work/web" {
		t.Errorf("reported %v, want each distinct cwd exactly once and no blanks", seen)
	}
}

// IndexedRepos is what doctor and the status surface read, so it has to reflect
// every tree rather than whichever was indexed last.
func TestIndexedReposListsEveryTree(t *testing.T) {
	e := &Engine{}
	a, b := t.TempDir(), t.TempDir()
	e.SetScorerForRepo(a, fakeScorer{"a"}, MatchConfig{})
	e.SetScorerForRepo(b, fakeScorer{"b"}, MatchConfig{})

	if got := e.IndexedRepos(); len(got) != 2 {
		t.Errorf("IndexedRepos() = %v, want both trees: a status that names one "+
			"index reads as a daemon that only covers one project", got)
	}
}
