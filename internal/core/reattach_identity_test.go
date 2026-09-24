package core

import (
	"testing"
	"time"
)

// A REATTACH STATES WHAT IT KNOWS, NOT EVERYTHING THAT IS TRUE.
//
// Reported from live use: an agent that had reattached to its own seat three
// times in eleven hours noticed its own model field going stale, and traced it
// to re-sending the registration payload by hand each time. The board was the
// cause rather than the cure. Every reattach path assigned op.Agent wholesale,
// so a returning session that mentioned a cwd and a surface silently cleared
// model, provider and title. The agent then reads its own row, copies the gaps
// forward, and the board forgets who its agents are one reattach at a time.
//
// `update` already merged. Three sites replaced and one merged, which is this
// repository's most expensive recurring shape.
func TestAReattachKeepsWhatItDidNotRestate(t *testing.T) {
	now := time.Now()
	s := NewState("n1", DefaultLimits())
	first := &Op{
		Kind: OpRegister, Name: "probe", NewToken: "t1", Nonce: "secret",
		SessionID: "sess-1", AgentKind: "persistent", PID: 4242, ProcStart: 1000,
		V7Semantics: true, Description: "the original description",
		Agent: &AgentInfo{
			CWD: "/repo", Model: "claude-opus-5", Provider: "anthropic",
			Title: "Probe", Surface: "claude-desktop",
		},
	}
	if _, _, err := s.Apply(first, now); err != nil {
		t.Fatal(err)
	}

	// The returning session: same nonce, new session id, new process, and an
	// identity payload with only what its harness happened to fill in.
	_, evs, err := s.Apply(&Op{
		Kind: OpRegister, Name: "probe", NewToken: "t2", Nonce: "secret",
		SessionID: "sess-2", AgentKind: "persistent", PID: 5151, ProcStart: 2000,
		V7Semantics: true,
		Agent:       &AgentInfo{CWD: "/repo", Surface: "claude-desktop"},
	}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}

	// SETUP FIRST, because a probe that never reaches the branch reports the
	// branch is fine. An earlier version of this test passed for exactly that
	// reason: without a pid and a kind the register was answered as a duplicate
	// retry, nothing was applied, and nothing was dropped because nothing
	// happened.
	if len(evs) == 0 {
		t.Fatal("setup: the reattach produced no events, so it did not happen")
	}
	l := s.Agents["probe"]
	if l.Token != "t2" {
		t.Fatalf("setup: the token did not rotate (%q), so this is not a reattach", l.Token)
	}

	for _, c := range []struct{ field, got, want string }{
		{"model", l.Agent.Model, "claude-opus-5"},
		{"provider", l.Agent.Provider, "anthropic"},
		{"title", l.Agent.Title, "Probe"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q after a reattach that did not restate it, want %q kept: "+
				"an omitted field means \"I did not say\", never \"clear it\"",
				c.field, c.got, c.want)
		}
	}
	if l.Description != "the original description" {
		t.Errorf("description = %q, want it kept", l.Description)
	}
}

// AND A MOVE IS STILL A MOVE. Merging must not turn into "nothing ever
// changes": an agent that restates a field has restated it, and the location
// group travels together so a moved agent does not keep the repository it used
// to be in. That rule is mergeIdentity's, and this is what stops a future
// simplification from quietly dropping it.
func TestAReattachStillTakesWhatItDoesRestate(t *testing.T) {
	now := time.Now()
	s := NewState("n1", DefaultLimits())
	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "probe", NewToken: "t1", Nonce: "secret",
		SessionID: "sess-1", AgentKind: "persistent", PID: 4242, ProcStart: 1000,
		V7Semantics: true,
		Agent: &AgentInfo{
			CWD: "/repo", Model: "claude-opus-5", Title: "Probe", Project: "repo",
		},
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "probe", NewToken: "t2", Nonce: "secret",
		SessionID: "sess-2", AgentKind: "persistent", PID: 5151, ProcStart: 2000,
		V7Semantics: true,
		Agent:       &AgentInfo{CWD: "/elsewhere", Project: "elsewhere", Model: "claude-fable-5-1"},
	}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	l := s.Agents["probe"]
	if l.Agent.Model != "claude-fable-5-1" {
		t.Errorf("model = %q: a restated field must be taken", l.Agent.Model)
	}
	if l.Agent.CWD != "/elsewhere" {
		t.Errorf("cwd = %q: an agent that moved has moved", l.Agent.CWD)
	}
	if l.Agent.Project != "elsewhere" {
		t.Errorf("project = %q: the location group travels with the cwd, or the row "+
			"describes where the agent used to be", l.Agent.Project)
	}
	if l.Agent.Title != "Probe" {
		t.Errorf("title = %q: still kept, because it was not restated", l.Agent.Title)
	}
}
