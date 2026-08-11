package engine

import (
	"sync"

	"github.com/agenxy/lanes/internal/core"
	"github.com/agenxy/lanes/internal/paths"
)

// repoLens answers core's "are these two agents in one repository" from Git,
// using identities resolved BEFORE the state loop was entered.
//
// The resolution has to happen out here. Matching runs on the loop, and
// paths.Identify shells out to `git rev-parse` behind a one-second timeout on a
// cache miss: a cold cwd would hold up every other agent on the board for as
// long as Git took to answer. So the engine resolves each directory once, off
// the loop, and core is handed a lookup that cannot block and cannot fail.
//
// Directories with no entry answer "no evidence" rather than "different", which
// is the whole reason core asks for three-valued truth: a lane registered
// without a cwd, or one that arrived between the resolve and the read, must not
// be treated as positively somewhere else.
type repoLens struct {
	ids map[string]paths.RepoID
}

// newRepoLens resolves every directory it is given, concurrently.
//
// Concurrently because the cost is a cold `git rev-parse` bounded at one second
// EACH: resolved in turn, a board of a dozen lanes that Git has not been asked
// about yet would put twelve seconds in front of one set_slot, and set_slot is
// the call agents make constantly. Fanning out bounds the whole thing to roughly
// one timeout. Repeats are free: paths.Identify is memoised process-wide behind
// a mutex, so this is a first-encounter cost per directory, not a per-call one.
//
// An empty string is skipped: "did not say where it is" is not a directory to
// ask Git about.
func newRepoLens(dirs []string) core.RepoLens {
	unique := make(map[string]bool, len(dirs))
	for _, d := range dirs {
		if d != "" {
			unique[d] = true
		}
	}
	if len(unique) == 0 {
		return nil // nobody told us where they are; let core reason about paths
	}
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		ids = make(map[string]paths.RepoID, len(unique))
	)
	for d := range unique {
		wg.Add(1)
		go func(dir string) {
			defer wg.Done()
			id := paths.Identify(dir)
			mu.Lock()
			ids[dir] = id
			mu.Unlock()
		}(d)
	}
	wg.Wait()
	return &repoLens{ids: ids}
}

func (l *repoLens) SameRepo(aCWD, bCWD string) (same, known bool) {
	a, haveA := l.ids[aCWD]
	b, haveB := l.ids[bCWD]
	if !haveA || !haveB {
		return false, false
	}
	return paths.SameRepo(a, b)
}
