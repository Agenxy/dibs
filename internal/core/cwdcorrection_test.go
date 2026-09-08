package core

import (
	"testing"
)

// An agent must be able to correct the one field the board blames.
//
// Reported from a live board. Re-register with the same name and nonce and a
// CORRECTED cwd: the response says `resumed: true` and the board keeps the old
// value, because register short-circuits a same-nonce retry inside one TTL and
// returns the original result without applying anything. That is right for a
// retried registration after a lost response, and silently a no-op for a
// correction spelled the same way. PID already had an escape hatch here
// (no_process); cwd had none, and `update` carried no cwd at all.
//
// It is the worst field to have stranded: the matching hint names the cwd when
// a path cannot be read, so the one field an agent is told is at fault was the
// one field it could not fix without abandoning its identity and registering a
// sibling, which is how a board fills up with -2 rows.
//
// THE DERIVED FIELDS MOVE WITH IT. project and the repo identity are resolved
// FROM the cwd by the server, so applying the cwd alone would leave an agent
// whose recorded repository describes where it used to be. They travel as one
// group, and only when a cwd arrives, which is what keeps "an agent may not
// assert what repository it lives in" true.
func TestUpdateCanCorrectTheWorkingDirectory(t *testing.T) {
	s := NewState("t", DefaultLimits())
	reg(t, s, "mover", "tok", t0)

	l := s.Agents["mover"]
	l.Agent = &AgentInfo{CWD: "/wrong/place", Project: "wrong", RepoDir: "/wrong/place/.git"}

	res := mustApply(t, s, &Op{
		Kind: OpUpdate, Token: "tok", Description: "unchanged",
		// The shape the ingress produces: the agent asserted a cwd and the
		// server resolved the rest before the op was submitted.
		Agent: &AgentInfo{
			CWD: "/right/place", Project: "right", RepoDir: "/right/place/.git",
			RepoRemote: "github.com/agenxy/dibs",
		},
	}, t0)

	if got := s.Agents["mover"].Agent.CWD; got != "/right/place" {
		t.Fatalf("update did not correct the cwd: still %q. There is then no way to "+
			"fix it in-session at all, and it is the field the matching hint blames", got)
	}
	if got := s.Agents["mover"].Agent.Project; got != "right" {
		t.Errorf("the cwd moved and the project did not (%q), so the board now says "+
			"this agent works in a directory belonging to a different project", got)
	}
	if got := s.Agents["mover"].Agent.RepoDir; got != "/right/place/.git" {
		t.Errorf("the cwd moved and the repo dir did not: %q", got)
	}
	if got := s.Agents["mover"].Agent.RepoRemote; got != "github.com/agenxy/dibs" {
		t.Errorf("the cwd moved and the remote did not: %q", got)
	}
	// And it reports what it changed, because a correction nothing confirms is
	// indistinguishable from the silent no-op this exists to replace.
	changed, _ := res["identity"].([]string)
	var saidCWD bool
	for _, c := range changed {
		if c == "cwd" {
			saidCWD = true
		}
	}
	if !saidCWD {
		t.Errorf("the result does not report cwd among what changed (%v), so the "+
			"caller cannot tell a correction from the no-op it is replacing", changed)
	}
}

// An update that carries no cwd must not disturb the one on record.
//
// Every ordinary update sends model, branch or title and no cwd. If an absent
// cwd read as "clear it", the common call would erase the agent's location and
// its whole repo identity with it, which is a far worse bug than the one above.
func TestAnUpdateWithoutACwdLeavesItAlone(t *testing.T) {
	s := NewState("t", DefaultLimits())
	reg(t, s, "stayer", "tok", t0)
	s.Agents["stayer"].Agent = &AgentInfo{CWD: "/home/work", Project: "work"}

	mustApply(t, s, &Op{
		Kind: OpUpdate, Token: "tok", Description: "still here",
		Agent: &AgentInfo{Branch: "feature/x"},
	}, t0)

	if got := s.Agents["stayer"].Agent.CWD; got != "/home/work" {
		t.Errorf("an ordinary update erased the cwd: %q", got)
	}
	if got := s.Agents["stayer"].Agent.Project; got != "work" {
		t.Errorf("an ordinary update erased the project: %q", got)
	}
}
