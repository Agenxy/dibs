package engine

import (
	"path/filepath"
	"strings"

	lanespaths "github.com/agenxy/lanes/internal/paths"
)

// An agent working in a different repository cannot be duplicating work in this
// one.
//
// # The bug this closes
//
// Matching scores every declaration against ONE index — the repository the
// operator pointed `-match-repo` at. Nothing consulted which repository the
// declaring agent was actually in, even though the lane records its cwd.
//
// So an agent working on an unrelated project got scored against a repository it
// has no part in, and the only files that can match across two unrelated trees
// are the generic ones every project has: Justfile, ci.yml, README.md. Those are
// exactly the files that co-change with everything, so they carry almost no
// signal about WHAT anybody is doing while looking like solid evidence.
//
// It was reported by an agent within minutes of the first real fleet: it had
// merely READ one file in the other repository as a format reference, and was
// auto-joined to that repository's coordination lane on the strength of a
// Justfile and a ci.yml. Its own summary was better than the one here — "the
// shared-file evidence is generic".
//
// # Why a hard gate rather than a score penalty
//
// A penalty invites tuning, and the underlying fact is not a matter of degree:
// two agents in different repositories are not doing the same work, whatever
// their vocabulary has in common. Scoring them at all is the error.
//
// The declaration is still SURFACED — the agent is told what it looks like and
// can join deliberately, because a monorepo checked out twice, or a worktree,
// are real and Lanes should not pretend to know better. What it will not do is
// join them automatically, which is the part that costs an agent its attention
// for nothing.
//
// # Not knowing is not evidence
//
// A lane with no cwd — a plain HTTP client, an older harness — is NOT gated. The
// question this answers is "do I have positive evidence this agent is somewhere
// else", and an absent cwd is not that. Treating unknown as foreign would
// silently disable auto-join for every client that does not report one, which is
// a worse failure than the one being fixed and would look exactly like matching
// being broken.
func inMatchedRepo(agentCWD, matchRepo string) bool {
	if agentCWD == "" || matchRepo == "" {
		return true // no evidence either way; do not gate on ignorance
	}
	repo := lanespaths.Canonical(matchRepo)
	cwd := lanespaths.Canonical(agentCWD)
	if repo == "" || cwd == "" {
		return true
	}
	if cwd == repo {
		return true
	}
	// A subdirectory counts, including a worktree checked out beneath the repo.
	// Compared with a separator appended so that /repo-other does not read as
	// living inside /repo.
	return strings.HasPrefix(cwd, repo+string(filepath.Separator))
}
