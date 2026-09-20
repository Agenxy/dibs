package core

import (
	"testing"
	"time"
)

// inRepo registers an agent that is working in one checkout of a repository.
//
// commonDir is what makes two checkouts ONE repository (git's --git-common-dir
// is shared by every linked worktree); root is the checkout's own top level,
// which is what the claim path is measured from. Separating them is the whole
// arrangement under test: same repository, different absolute paths.
func inRepo(t *testing.T, s *State, name, token, commonDir, root, remote string, now time.Time) *Agent {
	t.Helper()
	res, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: name, NewToken: token,
		Agent: &AgentInfo{
			CWD: root, RepoDir: commonDir, RepoRoot: root, RepoRemote: remote,
		},
	}, now)
	if err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
	if _, _, err := s.Apply(&Op{Kind: OpAckBoard, Token: token}, now); err != nil {
		t.Fatalf("ack %s: %v", name, err)
	}
	return s.Agents[res["agent_id"].(string)]
}

// TWO LINKED WORKTREES ARE ONE REPOSITORY, and the claim path never knew.
//
// A claim was an absolute path compared as a raw string, so /a/wt1/pkg/x.go and
// /a/wt2/pkg/x.go did not overlap. They are the same tracked file, edited by
// two agents, which is the exact collision the product exists to report, and
// nothing was said. Not an exotic arrangement either: `git worktree add` is how
// this project reviews its own work, and AGENTS.md tells agents to use it to
// check a regression test against the commit before the fix.
//
// The declare-time signal already scoped by repository and had since it was
// written, so the WEAKER signal knew about the collision while the stronger one
// did not. The fix is the same rule from the other side: subtract each claim's
// own checkout root and compare what is left.
func TestClaimsCollideAcrossLinkedWorktreesOfOneRepository(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)

	one := inRepo(t, s, "one", "tok-1", "/a/main/.git", "/a/wt1", "git@example.com:acme/api", now)
	inRepo(t, s, "two", "tok-2", "/a/main/.git", "/a/wt2", "git@example.com:acme/api", now)

	got := mustApply(t, s, &Op{
		Kind: OpClaim, Token: "tok-1", Path: "/a/wt1/pkg/x.go", Mode: ClaimExclusive,
	}, now)
	if got["granted"] != true {
		t.Fatalf("setup: the first claim was refused: %v", got)
	}

	res := mustApply(t, s, &Op{
		Kind: OpClaim, Token: "tok-2", Path: "/a/wt2/pkg/x.go", Mode: ClaimExclusive,
	}, now)
	if res["granted"] != false {
		t.Fatalf("an exclusive claim was granted over the same tracked file that %s "+
			"already holds exclusively, because the two worktrees spell its path "+
			"differently. Both agents now believe they have it alone: %v", one.ID, res)
	}

	ov, _ := res["overlaps"].([]map[string]any)
	if len(ov) != 1 {
		t.Fatalf("the refusal named %d overlaps, want 1: %v", len(ov), res["overlaps"])
	}
	// WHICH RULE, because the holder's path is in another directory entirely and
	// a reader given only that would go and look at the wrong tree.
	if ov[0]["rule"] != OverlapByRepo {
		t.Errorf("the overlap does not say it matched by repository, so the reader "+
			"is handed a path under a root they never asked about with no "+
			"explanation: %v", ov[0])
	}
	if ov[0]["repo_path"] != "pkg/x.go" {
		t.Errorf("the overlap does not name the file as the repository names it: %v", ov[0])
	}
}

// And two agents in DIFFERENT repositories that happen to share a relative path
// are left alone. Every project has a pkg/x.go.
//
// This is the failure mode of the fix, and it is worse than the bug: a claim
// conflict between strangers is an agent stopping work nothing else is doing.
// Positive evidence of one repository is required, and two different common
// directories with two different remotes is positive evidence of separation.
func TestClaimsDoNotCollideBetweenDifferentRepositoriesSharingARelativePath(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)

	inRepo(t, s, "one", "tok-1", "/a/api/.git", "/a/api", "git@example.com:acme/api", now)
	inRepo(t, s, "two", "tok-2", "/b/web/.git", "/b/web", "git@example.com:acme/web", now)

	mustApply(t, s, &Op{
		Kind: OpClaim, Token: "tok-1", Path: "/a/api/pkg/x.go", Mode: ClaimExclusive,
	}, now)
	res := mustApply(t, s, &Op{
		Kind: OpClaim, Token: "tok-2", Path: "/b/web/pkg/x.go", Mode: ClaimExclusive,
	}, now)
	if res["granted"] != true {
		t.Fatalf("two agents in unrelated repositories were told they collide "+
			"because both projects contain a pkg/x.go. Stopping work nothing else "+
			"is doing is a worse outcome than the missed collision this rule was "+
			"added for: %v", res)
	}
}

// A path outside the agent's own checkout has no portable name, and the rule
// simply does not apply to it. Silence, not a guess.
func TestAClaimOutsideTheCheckoutGetsNoPortableName(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)

	inRepo(t, s, "one", "tok-1", "/a/main/.git", "/a/wt1", "git@example.com:acme/api", now)
	inRepo(t, s, "two", "tok-2", "/a/main/.git", "/a/wt2", "git@example.com:acme/api", now)

	mustApply(t, s, &Op{
		Kind: OpClaim, Token: "tok-1", Path: "/tmp/scratch", Mode: ClaimExclusive,
	}, now)
	for _, c := range s.Claims {
		if c.Path == "/tmp/scratch" && c.RepoPath != "" {
			t.Fatalf("a path outside the checkout was given the portable name %q. "+
				"A prefix that did not match cannot yield a relative path, and "+
				"inventing one would make /tmp/scratch collide with a real file", c.RepoPath)
		}
	}
	// And it collides with nothing in the other worktree.
	res := mustApply(t, s, &Op{
		Kind: OpClaim, Token: "tok-2", Path: "/a/wt2/scratch", Mode: ClaimExclusive,
	}, now)
	if res["granted"] != true {
		t.Errorf("an unrelated claim was refused: %v", res)
	}
}

// THE GUARD HAS TO AGREE WITH THE BOARD, and this is the half that bites.
//
// The board reporting a conflict while the enforcement path waves the edit
// through is the worse of the two to get wrong: an agent is told it holds a
// directory exclusively and the tool that exists to hold it open lets the other
// agent write. Both read the same rule now.
func TestTheGuardStopsAWriteThroughAnotherWorktree(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)

	one := inRepo(t, s, "one", "tok-1", "/a/main/.git", "/a/wt1", "git@example.com:acme/api", now)
	two := inRepo(t, s, "two", "tok-2", "/a/main/.git", "/a/wt2", "git@example.com:acme/api", now)

	mustApply(t, s, &Op{
		Kind: OpClaim, Token: "tok-1", Path: "/a/wt1/pkg", Mode: ClaimExclusive,
	}, now)

	v := s.GuardPath(two.ID, "/a/wt2/pkg/x.go", now)
	if v.Decision == GuardAllow {
		t.Fatalf("the guard allowed a write to a file %s holds exclusively, because "+
			"the writer reaches it through a different worktree. The board reports "+
			"this conflict and the enforcement path does not, which is the pair "+
			"disagreeing in the direction that costs work: %+v", one.ID, v)
	}
	if v.Holder != one.ID {
		t.Errorf("the verdict does not name the holder: %+v", v)
	}
}

// The guard still allows a write in an unrelated repository.
func TestTheGuardAllowsAWriteInAnotherRepository(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)

	inRepo(t, s, "one", "tok-1", "/a/api/.git", "/a/api", "git@example.com:acme/api", now)
	two := inRepo(t, s, "two", "tok-2", "/b/web/.git", "/b/web", "git@example.com:acme/web", now)

	mustApply(t, s, &Op{
		Kind: OpClaim, Token: "tok-1", Path: "/a/api/pkg", Mode: ClaimExclusive,
	}, now)

	if v := s.GuardPath(two.ID, "/b/web/pkg/x.go", now); v.Decision != GuardAllow {
		t.Errorf("the guard blocked an edit in an unrelated repository because both "+
			"projects contain a pkg: %+v", v)
	}
}

// repoPathOf must not treat a sibling directory as a child. /a/wt is not a
// prefix of /a/wt2 in any sense that matters, and the separator is what says so:
// the same prefix bug pathsOverlap exists to avoid, one level up.
func TestASiblingCheckoutIsNotInsideTheOtherOne(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)

	one := inRepo(t, s, "one", "tok-1", "/a/main/.git", "/a/wt", "git@example.com:acme/api", now)

	if got := repoPathOf(one, "/a/wt2/pkg/x.go"); got != "" {
		t.Errorf("a path in the sibling checkout /a/wt2 was read as %q inside /a/wt: "+
			"every file in one worktree would collide with a differently-named file "+
			"in the other", got)
	}
	if got := repoPathOf(one, "/a/wt/pkg/x.go"); got != "pkg/x.go" {
		t.Errorf("a path genuinely inside the checkout resolved to %q", got)
	}
}

// onHost is inRepo for an agent working on a NAMED computer.
func onHost(t *testing.T, s *State, name, token, host, commonDir, root, remote string, now time.Time) *Agent {
	t.Helper()
	res, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: name, NewToken: token,
		Agent: &AgentInfo{
			CWD: root, RepoDir: commonDir, RepoRoot: root, RepoRemote: remote, HostID: host,
		},
	}, now)
	if err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
	if _, _, err := s.Apply(&Op{Kind: OpAckBoard, Token: token}, now); err != nil {
		t.Fatalf("ack %s: %v", name, err)
	}
	return s.Agents[res["agent_id"].(string)]
}

// AN ABSOLUTE PATH IS ONLY ABSOLUTE ON ONE COMPUTER.
//
// One daemon serves agents on other machines: SPEC §16 ships that in v1, bind a
// reachable address and remote agents present the same bearer credential. Their
// paths then arrive in the same namespace as everyone else's, and
// /Users/kim/src/api on two laptops is two unrelated trees. The exclusive claim
// rule refused the second agent over files the first has never seen, and the
// refusal named a holder whose path the reader could go and look at, finding
// their own work.
//
// I first argued this case was unreachable and deferred it. That was wrong, and
// checking SPEC §16 before writing the sentence would have caught it: remote
// agents are a shipped flag, not a plan.
func TestTwoMachinesDoNotCollideOnAPathTheyBothHappenToHave(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)

	onHost(t, s, "here", "tok-1", "host-a", "/Users/kim/api/.git", "/Users/kim/api",
		"git@example.com:acme/api", now)
	onHost(t, s, "there", "tok-2", "host-b", "/Users/kim/other/.git", "/Users/kim/other",
		"git@example.com:acme/other", now)

	mustApply(t, s, &Op{
		Kind: OpClaim, Token: "tok-1", Path: "/Users/kim/api/pkg", Mode: ClaimExclusive,
	}, now)
	// The same STRING on the other machine, and a different directory entirely.
	res := mustApply(t, s, &Op{
		Kind: OpClaim, Token: "tok-2", Path: "/Users/kim/api/pkg", Mode: ClaimExclusive,
	}, now)
	if res["granted"] != true {
		t.Fatalf("an agent on another computer was refused a claim because a path on "+
			"THIS machine is spelled the same. Two filesystems, two meanings, and the "+
			"refusal points at a directory the reader cannot see: %v", res)
	}
}

// And the collision that DOES cross machines still fires: two clones of one
// project name the same file identically once each checkout root is subtracted.
// That is what the repository rule is for, and it is the case the whole
// cross-machine story exists to keep working.
func TestOneRepositoryStillCollidesAcrossTwoMachines(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)

	one := onHost(t, s, "here", "tok-1", "host-a", "/Users/kim/api/.git", "/Users/kim/api",
		"git@example.com:acme/api", now)
	onHost(t, s, "there", "tok-2", "host-b", "/srv/build/api/.git", "/srv/build/api",
		"git@example.com:acme/api", now)

	mustApply(t, s, &Op{
		Kind: OpClaim, Token: "tok-1", Path: "/Users/kim/api/pkg/x.go", Mode: ClaimExclusive,
	}, now)
	res := mustApply(t, s, &Op{
		Kind: OpClaim, Token: "tok-2", Path: "/srv/build/api/pkg/x.go", Mode: ClaimExclusive,
	}, now)
	if res["granted"] != false {
		t.Fatalf("two agents on different machines were both granted the same file of "+
			"the same repository. Nothing about a network makes that not a collision, "+
			"and %s believes it holds it alone: %v", one.ID, res)
	}
	ov, _ := res["overlaps"].([]map[string]any)
	if len(ov) != 1 || ov[0]["rule"] != OverlapByRepo {
		t.Errorf("the refusal does not say it matched by repository, which is the only "+
			"thing that makes a path under another machine's root intelligible: %v",
			res["overlaps"])
	}
}

// AN AGENT THAT SAID NOTHING ABOUT ITS MACHINE COLLIDES EXACTLY AS BEFORE.
//
// Absence of evidence is not difference, and this rule REMOVES collisions, so
// the conservative answer to "no idea" is to go on reporting. Every claim on
// every board written before the field existed is in that state, and a board
// that quietly stopped reporting conflicts on upgrade would be the worst
// possible way to ship this.
func TestAnAgentWithNoHostIDCollidesAsItAlwaysDid(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)

	// Neither states a host, which is what an older board looks like.
	inRepo(t, s, "one", "tok-1", "/a/api/.git", "/a/api", "git@example.com:acme/api", now)
	inRepo(t, s, "two", "tok-2", "/b/web/.git", "/b/web", "git@example.com:acme/web", now)

	mustApply(t, s, &Op{
		Kind: OpClaim, Token: "tok-1", Path: "/shared/tree", Mode: ClaimExclusive,
	}, now)
	res := mustApply(t, s, &Op{
		Kind: OpClaim, Token: "tok-2", Path: "/shared/tree/sub", Mode: ClaimExclusive,
	}, now)
	if res["granted"] != false {
		t.Fatalf("two agents that never said which machine they are on stopped "+
			"colliding on one absolute path. Unknown must behave as this board did "+
			"before the field existed, or an upgrade silently switches off the "+
			"conflicts it was reporting yesterday: %v", res)
	}

	// One of them stating a host is still not evidence of two machines.
	s2 := NewState("t", DefaultLimits())
	onHost(t, s2, "one", "tok-1", "host-a", "/a/api/.git", "/a/api", "git@example.com:acme/api", now)
	inRepo(t, s2, "two", "tok-2", "/b/web/.git", "/b/web", "git@example.com:acme/web", now)
	mustApply(t, s2, &Op{
		Kind: OpClaim, Token: "tok-1", Path: "/shared/tree", Mode: ClaimExclusive,
	}, now)
	if r := mustApply(t, s2, &Op{
		Kind: OpClaim, Token: "tok-2", Path: "/shared/tree/sub", Mode: ClaimExclusive,
	}, now); r["granted"] != false {
		t.Errorf("one agent naming its machine was read as proof the other is "+
			"somewhere else: %v", r)
	}
}

// The write guard reads the same rule, so it does not stop an edit on another
// computer's identically-spelled path.
func TestTheGuardDoesNotReachAcrossMachines(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)

	onHost(t, s, "here", "tok-1", "host-a", "/Users/kim/api/.git", "/Users/kim/api",
		"git@example.com:acme/api", now)
	there := onHost(t, s, "there", "tok-2", "host-b", "/Users/kim/other/.git",
		"/Users/kim/other", "git@example.com:acme/other", now)

	mustApply(t, s, &Op{
		Kind: OpClaim, Token: "tok-1", Path: "/Users/kim/api/pkg", Mode: ClaimExclusive,
	}, now)

	if v := s.GuardPath(there.ID, "/Users/kim/api/pkg/x.go", now); v.Decision != GuardAllow {
		t.Errorf("the guard denied a write on another computer because the path is "+
			"spelled the same here. The board and the guard agree, which is right, "+
			"and they were agreeing about the wrong thing: %+v", v)
	}
}

// A claim over the whole checkout covers every file in every other checkout
// of that repository.
//
// The root claim's repository-relative path is ".", and pathsOverlap does not
// treat "." as an ancestor of "pkg/x.go", so an exclusive claim over /clone-a
// let another agent take /clone-b/pkg/x.go in the same repository, and the
// guard shared the gap. The one claim that says "all of it" covered nothing
// across worktrees. Found by the pre-release review.
func TestAClaimOverTheCheckoutRootCoversTheOtherWorktree(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)

	inRepo(t, s, "one", "tok-1", "/a/main/.git", "/a/wt1", "git@example.com:acme/api", now)
	two := inRepo(t, s, "two", "tok-2", "/a/main/.git", "/a/wt2", "git@example.com:acme/api", now)

	got := mustApply(t, s, &Op{Kind: OpClaim, Token: "tok-1", Path: "/a/wt1", Mode: ClaimExclusive}, now)
	if got["granted"] != true {
		t.Fatalf("setup: the root claim was refused: %v", got)
	}
	res := mustApply(t, s, &Op{Kind: OpClaim, Token: "tok-2", Path: "/a/wt2/pkg/x.go", Mode: ClaimExclusive}, now)
	if res["granted"] != false {
		t.Fatalf("an exclusive claim over a file was granted to %s while another agent "+
			"holds the whole repository exclusively through its own worktree: %v", two.ID, res)
	}
	ov, _ := res["overlaps"].([]map[string]any)
	if len(ov) != 1 || ov[0]["rule"] != OverlapByRepo {
		t.Errorf("the refusal should name the repository rule: %v", res["overlaps"])
	}
	// And the guard, which shares the comparison.
	v := s.GuardPath(two.ID, "/a/wt2/pkg/x.go", now)
	if v.Decision != GuardDeny {
		t.Errorf("guard = %+v, want deny: the write lands in a repository somebody else holds whole", v)
	}
}

// A claim keeps the repository it was taken in when its holder moves on.
//
// The comparison read the holder's CURRENT identity, so an agent that
// claimed a file in project P and then updated its cwd to unrelated Q left
// the claim standing with its repository protection gone: another clone of
// P could take the same file exclusively. The claim now records which
// repository its relative path is in. Found by the pre-release review.
func TestAClaimKeepsItsRepositoryWhenTheHolderMoves(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)
	one := inRepo(t, s, "one", "tok-1", "/a/main/.git", "/a/wt1", "git@example.com:acme/api", now)
	inRepo(t, s, "two", "tok-2", "/b/main/.git", "/b/wt2", "git@example.com:acme/api", now)

	got := mustApply(t, s, &Op{Kind: OpClaim, Token: "tok-1", Path: "/a/wt1/pkg/x.go", Mode: ClaimExclusive}, now)
	if got["granted"] != true {
		t.Fatalf("setup: the first claim was refused: %v", got)
	}
	// The holder moves to an unrelated project. Its claim in acme/api stands.
	mustApply(t, s, &Op{Kind: OpUpdate, Token: "tok-1", KeepDescription: true, Agent: &AgentInfo{
		CWD: "/elsewhere/q", RepoDir: "/elsewhere/q/.git", RepoRoot: "/elsewhere/q", RepoRemote: "git@example.com:acme/q",
	}}, now)
	if len(s.Claims) != 1 || s.Claims[0].Agent != one.ID {
		t.Fatalf("setup: the claim should still stand: %+v", s.Claims)
	}
	res := mustApply(t, s, &Op{Kind: OpClaim, Token: "tok-2", Path: "/b/wt2/pkg/x.go", Mode: ClaimExclusive}, now)
	if res["granted"] != false {
		t.Fatalf("the same tracked file was granted exclusively to two while one still holds "+
			"it: one moving to another project must not strip the claim it left behind: %v", res)
	}
}

// A Git directory PATH is evidence of one repository on one machine only.
//
// SameProject compared RepoDir strings without asking which machines they
// were on, so two unrelated repositories at /workspace/repo/.git on two hosts
// collided over matching relative paths, with different remotes and
// different root commits, and the write guard blocked work the other machine
// has never seen. Found by the pre-release review.
func TestTheSameGitDirectoryPathOnTwoMachinesIsNotOneRepository(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)
	reg := func(name, token, host, remote, roots string) {
		t.Helper()
		if _, _, err := s.Apply(&Op{Kind: OpRegister, Name: name, NewToken: token, Agent: &AgentInfo{
			HostID: host, CWD: "/workspace/repo", RepoDir: "/workspace/repo/.git", RepoRoot: "/workspace/repo",
			RepoRemote: remote, RepoRoots: roots,
		}}, now); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.Apply(&Op{Kind: OpAckBoard, Token: token}, now); err != nil {
			t.Fatal(err)
		}
	}
	reg("a", "ta", "host-a", "git@example.com:acme/api", "aaaa")
	reg("b", "tb", "host-b", "git@example.com:other/thing", "bbbb")
	mustApply(t, s, &Op{Kind: OpClaim, Token: "ta", Path: "/workspace/repo/pkg/x.go", Mode: ClaimExclusive}, now)
	res := mustApply(t, s, &Op{Kind: OpClaim, Token: "tb", Path: "/workspace/repo/pkg/x.go", Mode: ClaimExclusive}, now)
	if res["granted"] != true {
		t.Fatalf("an exclusive claim in an unrelated repository on another machine was "+
			"refused because its .git happens to live at the same path: %v", res)
	}
	// And the same path with the SAME remote on two machines still collides:
	// that is the rule working, through the machine-independent facts.
	reg("c", "tc", "host-c", "git@example.com:acme/api", "cccc")
	res = mustApply(t, s, &Op{Kind: OpClaim, Token: "tc", Path: "/workspace/repo/pkg/x.go", Mode: ClaimExclusive}, now)
	if res["granted"] != false {
		t.Fatalf("a clone of the same remote on a third machine should collide: %v", res)
	}
}
