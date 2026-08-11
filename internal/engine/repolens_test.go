package engine

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func requireGit(t *testing.T) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	return git
}

func run(t *testing.T, git, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(git, append([]string{"-C", dir}, args...)...)
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// The case the fleet actually hits. Agents run in linked worktrees: this
// project's own sessions do, and a worktree lives wherever it was created,
// which is routinely nowhere near its checkout. To a prefix test those are two
// unrelated places, so repository-scoped identifiers between two agents in one
// project could never be trusted to act. Git says they are one repository.
func TestLensSeesOneRepositoryAcrossLinkedWorktrees(t *testing.T) {
	git := requireGit(t)
	root := t.TempDir()
	repo := filepath.Join(root, "checkout")
	worktree := filepath.Join(root, "elsewhere", "wt")
	run(t, git, t.TempDir(), "init", repo) // init INTO repo, from an unrelated cwd
	run(t, git, repo, "commit", "--allow-empty", "-m", "root")
	run(t, git, repo, "worktree", "add", "-b", "feature", worktree)

	lens := newRepoLens([]string{repo, worktree})
	if lens == nil {
		t.Fatal("no lens built from two real checkouts")
	}
	if same, known := lens.SameRepo(repo, worktree); !same || !known {
		t.Errorf("checkout vs its linked worktree = (%v,%v), want (true,true)", same, known)
	}
}

// A directory the engine never resolved must answer "no evidence", not
// "different". The lens is keyed by cwd, and a lane can register between the
// resolve and the read: reporting that as positively somewhere else would veto
// a match on the strength of a race.
func TestLensIsSilentAboutWhatItNeverResolved(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	lens := newRepoLens([]string{dir})
	if lens == nil {
		t.Skip("nothing resolved to ask about")
	}
	if same, known := lens.SameRepo(dir, "/somewhere/never/seen"); same || known {
		t.Errorf("unresolved directory = (%v,%v), want (false,false)", same, known)
	}
}

// Nobody said where they were, so there is nothing to consult and core should
// be handed nil rather than an empty lookup that answers "no" to everything.
func TestNoLocationsMeansNoLens(t *testing.T) {
	if l := newRepoLens(nil); l != nil {
		t.Errorf("newRepoLens(nil) = %v, want nil", l)
	}
	if l := newRepoLens([]string{"", ""}); l != nil {
		t.Errorf("newRepoLens(blanks) = %v, want nil", l)
	}
}
