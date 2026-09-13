package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// A restarted daemon indexes the trees its agents already work in. Indexing
// was triggered by registration alone, so every `dibs upgrade` left matching
// off for the whole board until somebody registered afresh; on this
// machine's own board that was hours, with 34 agents and no index. The ledger
// remembers where every agent works, and a restart replays it.
func TestARestartedDaemonIndexesTheTreesItsAgentsWorkIn(t *testing.T) {
	eng, ctx := testEngine(t)
	// Resolved, because git reports the real path and macOS's temp dir is a
	// symlink.
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup: git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-q", "--no-gpg-sign", "-m", "a")

	// The agent registered BEFORE this daemon's scorer existed: as replayed
	// from the ledger, nothing has heard of its tree.
	if _, err := eng.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "a", Agent: &core.AgentInfo{CWD: repo}}); err != nil {
		t.Fatalf("setup: register: %v", err)
	}
	if got := eng.WorkingDirectories(ctx); len(got) != 1 || got[0] != repo {
		t.Fatalf("WorkingDirectories = %v, want the agent's tree", got)
	}

	f := defaultScorerFlags()
	f.install(ctx, eng)
	deadline := time.Now().Add(15 * time.Second)
	for {
		st := eng.MatchStatus()
		// An INDEXED phase on this tree, and the scorer's own record of it:
		// a failure that names the repository it tried is not a restart
		// that recovered its index (the Codex review of this change).
		if st.Phase.Indexed() {
			if st.Repo != repo {
				t.Fatalf("matching came up on %q, want the agent's tree %q", st.Repo, repo)
			}
			f.discoverMu.Lock()
			indexed := f.indexed[repo]
			f.discoverMu.Unlock()
			if !indexed {
				t.Fatalf("matching reports %s on %s and the scorer never recorded the tree as indexed", st.Phase, repo)
			}
			if got := eng.IndexSuppliedBy(repo); got != "" {
				t.Fatalf("the daemon's own index is recorded as supplied by %q", got)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("matching is still %q %v after a restart: the tree the agent works in "+
				"was never indexed, so the board matches nothing until somebody registers again",
				st.Phase, st.Hint)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Thirty agents in subdirectories of one checkout are one tree: the boot
// pass resolves roots first and indexes each once, rather than starting a
// resolution per agent.
func TestBootIndexingResolvesRootsBeforeIndexing(t *testing.T) {
	eng, ctx := testEngine(t)
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup: git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q")
	for _, sub := range []string{"cmd", "internal", "docs"} {
		if err := os.MkdirAll(filepath.Join(repo, sub), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, sub, "a.go"), []byte("package a\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := eng.Do(ctx, &core.Op{Kind: core.OpRegister, Name: sub, Agent: &core.AgentInfo{CWD: filepath.Join(repo, sub)}}); err != nil {
			t.Fatalf("setup: register: %v", err)
		}
	}
	git("add", "-A")
	git("commit", "-q", "--no-gpg-sign", "-m", "a")
	if dirs := eng.WorkingDirectories(ctx); len(dirs) != 3 {
		t.Fatalf("WorkingDirectories = %v, want the three subdirectories", dirs)
	}

	f := defaultScorerFlags()
	f.indexKnownTrees(ctx, eng)
	// The pass records each cwd's root itself, before anything is indexed, so
	// this is deterministic: three cwds, one root. (Reading what
	// indexDiscovered records instead raced its goroutine on the Linux runner.)
	f.discoverMu.Lock()
	defer f.discoverMu.Unlock()
	for _, sub := range []string{"cmd", "internal", "docs"} {
		if got := f.rootOf[filepath.Join(repo, sub)]; got != repo {
			t.Errorf("after the boot pass %s resolves to %q, want the one repository %q", sub, got, repo)
		}
	}
	for cwd, r := range f.rootOf {
		if r != repo {
			t.Errorf("rootOf[%s] = %q: something other than the one repository was resolved", cwd, r)
		}
	}
}
