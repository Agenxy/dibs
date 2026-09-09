package mcp

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// The server derives WHERE an agent is working, and nothing checked that it did.
//
// This is the ingress half of the repository-scoped claim rule, and it is the
// half that fails silently. Deleting the line that records the checkout root
// leaves every core test green: the fold's rules are all conditioned on the
// field being present, so an empty one makes them politely not apply and the
// feature is simply absent, reporting success throughout. That is this
// repository's most expensive bug class (AGENTS.md: "a parameter you declare
// but never read is invisible from outside"), arrived at from the other end.
//
// Verified by deleting the assignment and watching the whole core suite pass.
//
// The other three fields were in the same position and are pinned here with it.
func TestTheServerRecordsWhereAnAgentIsWorking(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed; paths.Identify degrades to unknown by design")
	}
	root := t.TempDir()
	main := filepath.Join(root, "main")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command(git, args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(root, "init", "-q", "-b", "main", main)
	run(main, "commit", "-q", "--allow-empty", "-m", "first")
	run(main, "remote", "add", "origin", "git@example.com:acme/api.git")
	linked := filepath.Join(root, "wt1")
	run(main, "worktree", "add", "-q", linked)

	var here, there core.AgentInfo
	resolveLocation(&here, main)
	resolveLocation(&there, linked)

	if here.RepoDir == "" {
		t.Fatalf("the checkout was not identified at all, so this test proves "+
			"nothing about what is recorded: %+v", here)
	}
	// ONE repository, seen through two checkouts: the common directory is shared
	// and the roots are not. That difference is the entire arrangement the claim
	// rule turns on, so both halves are asserted rather than one.
	if here.RepoDir != there.RepoDir {
		t.Errorf("a linked worktree was recorded as a different repository:\n  %q\n  %q",
			here.RepoDir, there.RepoDir)
	}
	if here.RepoRoot == "" || there.RepoRoot == "" {
		t.Fatalf("the checkout root was not recorded, so every claim's portable name "+
			"is empty and the repository rule silently never applies:\n  %+v\n  %+v",
			here, there)
	}
	if here.RepoRoot == there.RepoRoot {
		t.Errorf("both checkouts recorded the same root %q, so subtracting it cannot "+
			"tell the two apart", here.RepoRoot)
	}
	if here.RepoRemote == "" {
		t.Errorf("the configured remote was not recorded: %+v", here)
	}
	if here.RepoRoots == "" {
		t.Errorf("the root commits were not recorded: %+v", here)
	}
	// The root is where the CWD is, canonicalised the same way, because the fold
	// subtracts one from the other as plain strings.
	if here.RepoRoot != here.CWD {
		t.Errorf("the recorded root %q and cwd %q are spelled differently, so the "+
			"prefix subtraction fails on the agent's own files",
			here.RepoRoot, here.CWD)
	}
}
