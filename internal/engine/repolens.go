package engine

import (
	"sync"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/paths"
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
// is the whole reason core asks for three-valued truth: an agent registered
// without a cwd, or one that arrived between the resolve and the read, must not
// be treated as positively somewhere else.
type repoLens struct {
	ids map[string]paths.RepoID
	// recorded is what the board holds about each directory's repository,
	// as its agent's bridge reported it at registration: the only evidence
	// there is for a directory on ANOTHER machine, which Git here cannot
	// see. Asking Git about a remote path answered "unknown", and the
	// fall-through then read two different checkout roots as two
	// repositories: two clones of one project on two machines declaring
	// pr:42 got "different repositories" and no join. Round thirteen of the
	// pre-release review. Consulted first; Git is the fallback for a local
	// directory whose agent recorded nothing.
	recorded map[string]*core.AgentInfo
	// ambiguous names a directory that agents on MORE THAN ONE machine work
	// in: a path repeats across machines, and keying the recorded identity
	// by path alone let one machine's project answer for the other's. Such
	// a directory is "unknown" here and Git (which sees only this machine)
	// is not asked either. Round fourteen of the pre-release review.
	ambiguous map[string]bool
}

// newRepoLens resolves every directory it is given, concurrently.
//
// Concurrently because the cost is a cold `git rev-parse` bounded at one second
// EACH: resolved in turn, a board of a dozen agents that Git has not been asked
// about yet would put twelve seconds in front of one declare, and declare is
// the call agents make constantly. Fanning out bounds the whole thing to roughly
// one timeout. Repeats are free: paths.Identify is memoised process-wide behind
// a mutex, so this is a first-encounter cost per directory, not a per-call one.
//
// An empty string is skipped: "did not say where it is" is not a directory to
// ask Git about.
func newRepoLens(dirs []string) core.RepoLens {
	return newRepoLensWith(dirs, nil)
}

// newRepoLensWith is newRepoLens with what the board recorded about each
// directory's repository; a directory with a recorded identity is answered
// from it and never asked of Git, which for a remote directory would answer
// about the wrong machine.
func newRepoLensWith(dirs []string, recorded map[string]*core.AgentInfo) core.RepoLens {
	return newRepoLensAmbiguous(dirs, recorded, nil)
}

// newRepoLensAmbiguous is newRepoLensWith with the directories that agents on
// more than one machine share, which no single recorded identity answers.
func newRepoLensAmbiguous(dirs []string, recorded map[string]*core.AgentInfo, ambiguous map[string]bool) core.RepoLens {
	unique := make(map[string]bool, len(dirs))
	for _, d := range dirs {
		if d != "" && !hasRecordedIdentity(recorded[d]) && !ambiguous[d] {
			unique[d] = true
		}
	}
	if len(unique) == 0 && len(recorded) == 0 && len(ambiguous) == 0 {
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
	return &repoLens{ids: ids, recorded: recorded, ambiguous: ambiguous}
}

func hasRecordedIdentity(info *core.AgentInfo) bool {
	return info != nil && (info.RepoDir != "" || info.RepoRemote != "" || info.RepoRoots != "")
}

func (l *repoLens) SameRepo(aCWD, bCWD string) (same, known bool) {
	if l.ambiguous[aCWD] || l.ambiguous[bCWD] {
		return false, false // agents on two machines share this path: no one answer
	}
	ra, rb := l.recorded[aCWD], l.recorded[bCWD]
	if hasRecordedIdentity(ra) && hasRecordedIdentity(rb) {
		// The board's own record, host-aware: a Git directory is a path and
		// a path is evidence on one machine only (core.SameProject).
		return core.SameProject(ra, rb), true
	}
	a, haveA := l.ids[aCWD]
	b, haveB := l.ids[bCWD]
	if !haveA || !haveB {
		return false, false
	}
	return paths.SameRepo(a, b)
}
