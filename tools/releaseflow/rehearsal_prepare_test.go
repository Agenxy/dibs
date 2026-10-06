package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// The preparation entry must make a candidate that changes a workflow safe for
// the scratch job's contents:write token to tag. The old scratch default branch
// intentionally lacks that workflow change, as it did in the failed v0.0.13 run.
func TestPrepareRehearsalMirrorsWorkflowChangingCandidate(t *testing.T) {
	c := fixture(t)
	old := c.sha
	scratch := filepath.Join(filepath.Dir(c.receiptPath), "scratch.git")
	git(t, "init", "--bare", scratch)
	git(t, "--git-dir="+scratch, "config", "--local", "gc.auto", "0")
	git(t, "--git-dir="+scratch, "config", "--local", "maintenance.auto", "false")
	git(t, "--git-dir="+scratch, "symbolic-ref", "HEAD", "refs/heads/main")
	git(t, "push", scratch, old+":refs/heads/main")
	git(t, "push", scratch, old+":refs/tags/rehearsal-proof-existing")
	write(t, workflowPath, "name: changed workflow fixture\n")
	git(t, "add", workflowPath)
	git(t, "commit", "-m", "change release workflow")
	c.sha = git(t, "rev-parse", "HEAD")
	git(t, "push", "origin", "HEAD:refs/heads/main")
	if diff := git(t, "diff", old, c.sha, "--", workflowPath); diff == "" {
		t.Fatal("fixture did not change a workflow relative to scratch main")
	}
	c.phase, c.target = "prepare-rehearsal", "rehearsal"
	scratchURL := "git@github.com:" + rehearsalRepository + ".git"
	run := func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
		if name == "gh" && strings.Join(args, " ") ==
			"api --paginate --slurp repos/Agenxy/dibs-release-rehearsal/releases?per_page=100" {
			return []byte(`[[{"id":77,"tag_name":"rehearsal-proof-existing"}]]`), nil
		}
		if name != "git" {
			t.Fatalf("unexpected command %s %v", name, args)
		}
		if len(args) == 3 && args[0] == "push" && strings.HasPrefix(args[2], c.sha+":refs/tags/") {
			if got := git(t, "ls-remote", scratch, "refs/heads/main"); got != c.sha+"\trefs/heads/main" {
				t.Fatalf("unique tag push preceded scratch main mirror: %s", got)
			}
		}
		if strings.Join(args, " ") == "remote get-url origin" {
			return []byte("git@github.com:Agenxy/dibs.git\n"), nil
		}
		mapped := append([]string(nil), args...)
		for i, arg := range mapped {
			if arg == scratchURL {
				mapped[i] = scratch
			}
		}
		return command(ctx, env, name, mapped...)
	}
	if err := execute(context.Background(), c, run); err != nil {
		t.Fatal(err)
	}
	tag := "refs/tags/rehearsal-v" + c.version + "-" + c.sha
	if got := git(t, "ls-remote", scratch, "refs/heads/main", tag); got != c.sha+"\trefs/heads/main\n"+c.sha+"\t"+tag {
		t.Fatalf("scratch default branch and unique tag must both be candidate: %s", got)
	}
	if got := git(t, "--git-dir="+scratch, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Fatalf("scratch default branch moved: %s", got)
	}
	if got := git(t, "ls-remote", scratch, "refs/tags/rehearsal-proof-existing"); got != old+"\trefs/tags/rehearsal-proof-existing" {
		t.Fatalf("existing proof tag changed: %s", got)
	}
}

func TestPrepareRehearsalResetsDivergedScratchMainWithLease(t *testing.T) {
	c := fixture(t)
	old := c.sha
	scratch := filepath.Join(filepath.Dir(c.receiptPath), "scratch.git")
	git(t, "init", "--bare", scratch)
	git(t, "--git-dir="+scratch, "config", "--local", "gc.auto", "0")
	git(t, "--git-dir="+scratch, "config", "--local", "maintenance.auto", "false")
	git(t, "--git-dir="+scratch, "symbolic-ref", "HEAD", "refs/heads/main")
	git(t, "push", scratch, old+":refs/heads/main")
	write(t, workflowPath, "name: candidate workflow\n")
	git(t, "add", workflowPath)
	git(t, "commit", "-m", "candidate workflow")
	c.sha = git(t, "rev-parse", "HEAD")
	git(t, "push", "origin", "HEAD:refs/heads/main")
	git(t, "switch", "-c", "scratch-diverged", old)
	write(t, "scratch-only.txt", "diverged\n")
	git(t, "add", "scratch-only.txt")
	git(t, "commit", "-m", "diverge scratch")
	diverged := git(t, "rev-parse", "HEAD")
	git(t, "push", scratch, "HEAD:refs/heads/main")
	git(t, "switch", "main")
	c.phase, c.target = "prepare-rehearsal", "rehearsal"
	scratchURL := "git@github.com:" + rehearsalRepository + ".git"
	run := func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
		if name == "gh" && strings.Join(args, " ") ==
			"api --paginate --slurp repos/Agenxy/dibs-release-rehearsal/releases?per_page=100" {
			return []byte(`[[]]`), nil
		}
		if name != "git" {
			t.Fatalf("unexpected command %s %v", name, args)
		}
		if strings.Join(args, " ") == "remote get-url origin" {
			return []byte("git@github.com:Agenxy/dibs.git\n"), nil
		}
		mapped := append([]string(nil), args...)
		for i, arg := range mapped {
			if arg == scratchURL {
				mapped[i] = scratch
			}
		}
		return command(ctx, env, name, mapped...)
	}
	if err := execute(context.Background(), c, run); err != nil {
		t.Fatal(err)
	}
	if got := git(t, "ls-remote", scratch, "refs/heads/main"); got != c.sha+"\trefs/heads/main" {
		t.Fatalf("scratch main not reset to exact candidate: %s (old %s)", got, diverged)
	}
	if got := git(t, "ls-remote", scratch, "refs/tags/rehearsal-v"+c.version+"-"+c.sha); got != c.sha+"\trefs/tags/rehearsal-v"+c.version+"-"+c.sha {
		t.Fatalf("unique tag not exact candidate: %s", got)
	}
}

func prepareLeaseFixture(t *testing.T) (config, string, string) {
	t.Helper()
	c := fixture(t)
	old := c.sha
	scratch := filepath.Join(filepath.Dir(c.receiptPath), "scratch.git")
	git(t, "init", "--bare", scratch)
	git(t, "--git-dir="+scratch, "config", "--local", "gc.auto", "0")
	git(t, "--git-dir="+scratch, "config", "--local", "maintenance.auto", "false")
	git(t, "--git-dir="+scratch, "symbolic-ref", "HEAD", "refs/heads/main")
	git(t, "push", scratch, old+":refs/heads/main")
	write(t, workflowPath, "name: candidate workflow\n")
	git(t, "add", workflowPath)
	git(t, "commit", "-m", "candidate workflow")
	c.sha = git(t, "rev-parse", "HEAD")
	git(t, "push", "origin", "HEAD:refs/heads/main")
	c.phase, c.target = "prepare-rehearsal", "rehearsal"
	return c, scratch, old
}

func TestPrepareRehearsalLeaseRefusesConcurrentScratchMove(t *testing.T) {
	c, scratch, old := prepareLeaseFixture(t)
	git(t, "switch", "-c", "other", old)
	write(t, "other.txt", "concurrent update\n")
	git(t, "add", "other.txt")
	git(t, "commit", "-m", "concurrent update")
	other := git(t, "rev-parse", "HEAD")
	git(t, "switch", "main")
	scratchURL := "git@github.com:" + rehearsalRepository + ".git"
	moved := false
	run := func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
		if name == "gh" {
			return []byte(`[[]]`), nil
		}
		if name != "git" {
			t.Fatalf("unexpected command %s %v", name, args)
		}
		if strings.Join(args, " ") == "remote get-url origin" {
			return []byte("git@github.com:Agenxy/dibs.git\n"), nil
		}
		if len(args) == 4 && args[0] == "push" &&
			args[1] == "--force-with-lease=refs/heads/main:"+old && !moved {
			git(t, "push", scratch, other+":refs/heads/main")
			moved = true
		}
		mapped := append([]string(nil), args...)
		for i, arg := range mapped {
			if arg == scratchURL {
				mapped[i] = scratch
			}
		}
		return command(ctx, env, name, mapped...)
	}
	err := execute(context.Background(), c, run)
	if !moved || err == nil || !strings.Contains(err.Error(), "lease") {
		t.Fatalf("concurrent scratch move did not defeat lease: moved=%v err=%v", moved, err)
	}
	if got := git(t, "ls-remote", scratch, "refs/heads/main"); got != other+"\trefs/heads/main" {
		t.Fatalf("concurrent main was overwritten: %s", got)
	}
	if got := git(t, "ls-remote", scratch, "refs/tags/rehearsal-v"+c.version+"-"+c.sha); got != "" {
		t.Fatalf("tag created after lease refusal: %s", got)
	}
}

func TestPrepareRehearsalRefusesChangingProofInventory(t *testing.T) {
	c, scratch, _ := prepareLeaseFixture(t)
	scratchURL := "git@github.com:" + rehearsalRepository + ".git"
	reads := 0
	run := func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
		if name == "gh" {
			reads++
			if reads == 1 {
				return []byte(`[[]]`), nil
			}
			return []byte(`[[{"id":77,"tag_name":"rehearsal-proof-concurrent"}]]`), nil
		}
		if name != "git" {
			t.Fatalf("unexpected command %s %v", name, args)
		}
		if strings.Join(args, " ") == "remote get-url origin" {
			return []byte("git@github.com:Agenxy/dibs.git\n"), nil
		}
		mapped := append([]string(nil), args...)
		for i, arg := range mapped {
			if arg == scratchURL {
				mapped[i] = scratch
			}
		}
		return command(ctx, env, name, mapped...)
	}
	err := execute(context.Background(), c, run)
	if reads != 2 || err == nil || !strings.Contains(err.Error(), "proof tags or releases changed") {
		t.Fatalf("changed proof set did not refuse unique tag: reads=%d err=%v", reads, err)
	}
	if got := git(t, "ls-remote", scratch, "refs/tags/rehearsal-v"+c.version+"-"+c.sha); got != "" {
		t.Fatalf("tag created after proof set changed: %s", got)
	}
}

func TestPrepareRehearsalRefusesBrokenAncestryProbe(t *testing.T) {
	c, scratch, old := prepareLeaseFixture(t)
	scratchURL := "git@github.com:" + rehearsalRepository + ".git"
	run := func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
		if name == "gh" {
			return []byte(`[[]]`), nil
		}
		if name != "git" {
			t.Fatalf("unexpected command %s %v", name, args)
		}
		if strings.Join(args, " ") == "remote get-url origin" {
			return []byte("git@github.com:Agenxy/dibs.git\n"), nil
		}
		if len(args) > 0 && args[0] == "merge-base" {
			return nil, errors.New("ancestry probe failed for an unrelated reason")
		}
		mapped := append([]string(nil), args...)
		for i, arg := range mapped {
			if arg == scratchURL {
				mapped[i] = scratch
			}
		}
		return command(ctx, env, name, mapped...)
	}
	err := execute(context.Background(), c, run)
	if err == nil || !strings.Contains(err.Error(), "cannot determine scratch main ancestry") {
		t.Fatalf("broken ancestry probe was treated as divergence: %v", err)
	}
	if got := git(t, "ls-remote", scratch, "refs/heads/main"); got != old+"\trefs/heads/main" {
		t.Fatalf("scratch main moved after broken ancestry probe: %s", got)
	}
	if got := git(t, "ls-remote", scratch, "refs/tags/rehearsal-v"+c.version+"-"+c.sha); got != "" {
		t.Fatalf("tag created after broken ancestry probe: %s", got)
	}
}

func TestPrepareRehearsalNeverTargetsProduction(t *testing.T) {
	c := config{destination: publicationTarget{repository: repository, rehearsal: true}}
	run := func(context.Context, []string, string, ...string) ([]byte, error) {
		t.Fatal("production target reached a subprocess")
		return nil, nil
	}
	if err := prepareRehearsalRemote(context.Background(), c, run); err == nil {
		t.Fatal("production repository was accepted as a scratch mirror")
	}
}
