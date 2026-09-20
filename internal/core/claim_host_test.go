package core

import (
	"testing"
	"time"
)

// A claim stays on the machine it was taken on, whatever its holder says
// about itself afterwards.
//
// The absolute-path rule asked whether the CLAIMANT and the HOLDER are on
// different machines, reading the holder's identity now. A holder that
// claimed /tmp/shared on host A and then updated its identity to host B left
// the claim standing with its path protection gone: the next agent on A took
// the same path exclusively, and a claim outside any checkout has no
// repository rule to catch it. The host is recorded on the claim when it is
// granted, like the repository is, and the rule reads that. Found by the
// pre-release review, round three.
func TestAClaimKeepsTheHostItWasTakenOn(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)

	onHost(t, s, "mover", "tok-1", "host-a", "", "", "", now)
	onHost(t, s, "stayer", "tok-2", "host-a", "", "", "", now)

	mustApply(t, s, &Op{Kind: OpClaim, Token: "tok-1", Path: "/tmp/shared", Mode: ClaimExclusive}, now)
	// The holder now says it is somewhere else.
	mustApply(t, s, &Op{Kind: OpUpdate, Token: "tok-1", Agent: &AgentInfo{CWD: "/tmp", HostID: "host-b"}}, now)

	res := mustApply(t, s, &Op{Kind: OpClaim, Token: "tok-2", Path: "/tmp/shared", Mode: ClaimExclusive}, now)
	if res["granted"] != false {
		t.Fatalf("a claim taken on host-a stopped protecting its path on host-a because its "+
			"holder later reported host-b: %v", res)
	}
}

// A renewal restates the whole claim: the relative path AND the repository it
// is relative to, together. Updating one and keeping the other combined a new
// path with an old project, so a holder that moved to another project at the
// same absolute path renewed a claim that still named the project it had
// left: the old project stayed protected against nobody, and a clone of the
// new one was granted the same file. Found by the pre-release review, round
// three.
func TestARenewalRestatesTheRepositoryWithThePath(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)

	holder := inRepo(t, s, "holder", "tok-1", "/work/app/.git", "/work/app", "git@example.com:acme/old", now)
	mustApply(t, s, &Op{Kind: OpClaim, Token: "tok-1", Path: "/work/app/pkg/x.go", Mode: ClaimExclusive}, now)

	// The same directory now holds a checkout of another project.
	mustApply(t, s, &Op{Kind: OpUpdate, Token: "tok-1", Agent: &AgentInfo{
		CWD: "/work/app", RepoDir: "/work/app/.git", RepoRoot: "/work/app", RepoRemote: "git@example.com:acme/new",
	}}, now)
	mustApply(t, s, &Op{Kind: OpClaim, Token: "tok-1", Path: "/work/app/pkg/x.go", Mode: ClaimExclusive}, now.Add(time.Minute))

	var c *Claim
	for _, cl := range s.Claims {
		if cl.Agent == holder.ID {
			c = cl
		}
	}
	if c == nil || c.Repo == nil {
		t.Fatalf("setup: the renewed claim has no repository recorded: %+v", c)
	}
	if c.Repo.RepoRemote != "git@example.com:acme/new" {
		t.Errorf("renewed claim names repository %q, want the project the holder is in now: "+
			"the relative path was restated and the repository it is relative to was not", c.Repo.RepoRemote)
	}

	// The consequence: a clone of the NEW project collides on that file.
	inRepo(t, s, "clone", "tok-2", "/elsewhere/new/.git", "/elsewhere/new", "git@example.com:acme/new", now)
	res := mustApply(t, s, &Op{Kind: OpClaim, Token: "tok-2", Path: "/elsewhere/new/pkg/x.go", Mode: ClaimExclusive}, now.Add(time.Minute))
	if res["granted"] != false {
		t.Errorf("a clone of the project the holder moved to was granted the file the holder "+
			"renewed its claim on: %v", res)
	}
}

// A declaration covering a whole checkout overlaps work in a subdirectory
// of another checkout of the same repository.
//
// The repository half of the declared-paths signal compared relative names
// with pathsOverlap, which reads "." and "pkg/x.go" as two strings sharing
// no prefix. So an agent declaring its whole worktree was never told about
// an agent working in pkg/ of another worktree, while the claim rule beside
// it had already learned that "." is the checkout and an ancestor of every
// path in it (repoPathsOverlap). Found by the pre-release review, round
// three.
func TestAWholeCheckoutDeclarationOverlapsASubdirectoryInAnotherWorktree(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)

	inRepo(t, s, "narrow", "tok-1", "/repo/.git", "/repo/wt1", "git@example.com:acme/api", now)
	inRepo(t, s, "wide", "tok-2", "/repo/.git", "/repo/wt2", "git@example.com:acme/api", now)
	mustApply(t, s, &Op{Kind: OpSetSlot, Token: "tok-1", Text: "pkg work", Dirs: []string{"/repo/wt1/pkg/x.go"}}, now)

	res := mustApply(t, s, &Op{Kind: OpSetSlot, Token: "tok-2", Text: "whole tree", Dirs: []string{"/repo/wt2"}}, now)
	ov, _ := res["overlaps"].([]SlotOverlap)
	if len(ov) != 1 || ov[0].Signal != SignalSamePaths {
		t.Fatalf("declaring a whole worktree beside an agent in pkg/ of another worktree of the "+
			"same repository reported %v, want one same-paths overlap", res["overlaps"])
	}
}

// Two unrelated repositories at one path on two machines do not share an
// objective. differentProjects read equal Git-directory strings as one
// repository without asking which machine, so two strangers declaring
// issue:42 on two hosts were told they were duplicating each other's work,
// with distinct remotes and root commits recorded on both. Round eight of
// the pre-release review.
func TestSharedReferencesDoNotCrossMachinesOnAPathTheyBothHave(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)

	for _, a := range []struct{ name, tok, host, remote, roots string }{
		{"here", "tok-1", "host-a", "git@example.com:acme/api", "r-api"},
		{"there", "tok-2", "host-b", "git@example.com:other/web", "r-web"},
	} {
		mustApply(t, s, &Op{
			Kind: OpRegister, Name: a.name, NewToken: a.tok,
			Agent: &AgentInfo{
				CWD: "/workspace/repo", HostID: a.host, RepoDir: "/workspace/repo/.git",
				RepoRoot: "/workspace/repo", RepoRemote: a.remote, RepoRoots: a.roots,
			},
		}, now)
		mustApply(t, s, &Op{Kind: OpAckBoard, Token: a.tok}, now)
	}
	mustApply(t, s, &Op{Kind: OpSetSlot, Token: "tok-1", Text: "issue 42", Refs: []string{"issue:42"}}, now)

	res := mustApply(t, s, &Op{Kind: OpSetSlot, Token: "tok-2", Text: "our issue 42", Refs: []string{"issue:42"}}, now)
	if ov, _ := res["overlaps"].([]SlotOverlap); len(ov) != 0 {
		t.Fatalf("two different projects on two machines, at one path, were told they share an "+
			"objective: %v", ov)
	}
}
