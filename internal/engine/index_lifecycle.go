package engine

// RemoveScorerForRepo drops a derived repository index after its last agent has
// left. The ledger remains the source of truth; this only releases the in-memory
// scorer and prevents a lifetime repository count from exhausting the ceiling.
func (e *Engine) RemoveScorerForRepo(repo string) {
	e.matchMu.Lock()
	defer e.matchMu.Unlock()
	delete(e.scorers, repo)
	if len(e.scorers) == 0 {
		e.scorer = nil
		e.matchCfg.Repo = ""
		return
	}
	if nextScorer, ok := e.scorers[e.matchCfg.Repo]; ok {
		e.scorer = nextScorer
		return
	}
	for nextRepo, nextScorer := range e.scorers {
		e.scorer = nextScorer
		e.matchCfg.Repo = nextRepo
		return
	}
}
