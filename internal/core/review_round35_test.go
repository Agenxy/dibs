package core

import (
	"testing"
	"time"
)

// The location group applied only when the cwd differed: an agent that
// registered in a directory before `git init`, or whose repository changed
// its remote, corrected with the same cwd, the ingress resolved the new
// repository, and the fold discarded it and reported success with the old
// identity. The group applies when any of its fields differ.
func TestAnUpdateInTheSameDirectoryTakesTheRederivedRepository(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)
	mustApply(t, s, &Op{Kind: OpRegister, Name: "w", NewToken: "tok", V7Semantics: true, Agent: &AgentInfo{CWD: "/work/app"}}, now)
	if s.Agents["w"].Agent.RepoDir != "" {
		t.Fatal("setup: the fixture registered with a repository already")
	}
	// What the ingress resolves after `git init` in the same directory.
	res := mustApply(t, s, &Op{Kind: OpUpdate, Token: "tok", Description: "d", V7Semantics: true, Agent: &AgentInfo{
		CWD: "/work/app", Project: "app", RepoDir: "/work/app/.git", RepoRemote: "github.com/x/app", RepoRoots: "abc",
	}}, now.Add(time.Minute))
	got := s.Agents["w"].Agent
	if got.RepoDir != "/work/app/.git" || got.RepoRemote != "github.com/x/app" || got.Project != "app" || got.RepoRoots != "abc" {
		t.Fatalf("update(cwd unchanged) reported %v and left the row at %+v: the repository the ingress "+
			"resolved was discarded and the old identity reported as success", res["changed"], got)
	}
	// And a remote that changed, same directory, same repository.
	mustApply(t, s, &Op{Kind: OpUpdate, Token: "tok", Description: "d", V7Semantics: true, Agent: &AgentInfo{
		CWD: "/work/app", Project: "app", RepoDir: "/work/app/.git", RepoRemote: "github.com/y/app", RepoRoots: "abc",
	}}, now.Add(2*time.Minute))
	if got := s.Agents["w"].Agent.RepoRemote; got != "github.com/y/app" {
		t.Fatalf("a changed remote in the same directory was discarded: row still says %q", got)
	}
}
