package engine

import "sort"

// RemoveScorerForRepo drops a derived repository index once no agent is working
// in that tree any more.
//
// The ledger stays the source of truth: this releases an in-memory index and
// nothing else, so the count that bounds indexing stops being a LIFETIME one.
// Without it a machine that had seen the ceiling's worth of repositories
// stopped matching for the next one forever, including a tree it was actively
// working in. Issue #40.
func (e *Engine) RemoveScorerForRepo(repo string) {
	e.matchMu.Lock()
	defer e.matchMu.Unlock()
	if _, held := e.scorers[repo]; !held {
		return
	}
	delete(e.scorers, repo)
	delete(e.indexes, repo)

	// THE FALLBACK PAIR HAS TO NAME ONE TREE.
	//
	// e.scorer and e.matchCfg.Repo are the single-scorer pair used by callers
	// with no agent in hand, such as Predict from the CLI. They are an index
	// and the name of the tree it was mined from, so replacing one without the
	// other would answer questions about repository A out of repository B's
	// history, which is the confident nonsense scorerFor exists to prevent.
	// Only .Repo is per-tree here; the thresholds are shared policy, as
	// SetScorerForRepo says.
	if e.matchCfg.Repo != repo {
		return // the pair still names a tree we hold
	}
	if len(e.scorers) == 0 {
		e.scorer, e.matchCfg.Repo = nil, ""
		return
	}
	// Sorted rather than whatever the map yields first: two daemons replaying
	// the same history should not disagree about which tree the fallback names,
	// and a test that asserts it should not be flaky.
	rest := make([]string, 0, len(e.scorers))
	for r := range e.scorers {
		rest = append(rest, r)
	}
	sort.Strings(rest)
	e.matchCfg.Repo = rest[0]
	e.scorer = e.scorers[rest[0]]
}
