package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Two worktrees of one repository are ONE repository for matching.
//
// This is the case worktrees exist for: a fleet parallelises work on a single
// repository by giving each agent its own checkout, precisely so they do not
// collide. `git rev-parse --show-toplevel` answers with the WORKTREE, so each
// one was indexed under its own root, scored by its own model, and never
// compared with any of its siblings. Dibs was blind exactly where it was most
// needed, and mined the same history once per worktree while being so.
//
// The test that would have caught it never existed, which is why this one does.
func TestWorktreesOfOneRepositoryShareARoot(t *testing.T) {
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup: git %v: %v: %s", args, err, out)
		}
	}

	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(repo, "add", "a.txt")
	git(repo, "commit", "-qm", "first")

	// Two linked worktrees, which is how a fleet actually works.
	wtA := filepath.Join(base, "wt-a")
	wtB := filepath.Join(base, "wt-b")
	git(repo, "worktree", "add", "--detach", wtA, "HEAD")
	git(repo, "worktree", "add", "--detach", wtB, "HEAD")

	ctx := context.Background()
	rootA, err := repoRootOf(ctx, wtA)
	if err != nil {
		t.Fatal(err)
	}
	rootB, err := repoRootOf(ctx, wtB)
	if err != nil {
		t.Fatal(err)
	}
	rootMain, err := repoRootOf(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}

	if rootA != rootB {
		t.Errorf("two worktrees of one repository resolved to different roots:\n  %s\n  %s\n"+
			"Their agents are scored by different models and never compared, which is "+
			"the collision matching exists to catch", rootA, rootB)
	}
	if rootA != rootMain {
		t.Errorf("a worktree (%s) does not share the repository's own root (%s), so an "+
			"agent in a worktree is never matched against one in the main checkout",
			rootA, rootMain)
	}
}

// A directory that is not a linked worktree is unaffected: the repository's own
// checkout still resolves to itself, and a non-repository still errors.
func TestAPlainCheckoutIsUnchanged(t *testing.T) {
	base := t.TempDir()
	if _, err := repoRootOf(context.Background(), base); err == nil {
		t.Error("a directory that is not a git repository resolved to a root")
	}
}
