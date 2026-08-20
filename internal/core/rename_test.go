package core

import (
	"errors"
	"testing"
)

// An agent may change what it says about itself, but never its address.
//
// Agents pick a name in their first seconds, before they know what they will be
// doing, and a board of "agent", "claude-1" and "worker" is a board a human
// cannot read. So the descriptive half has to be mutable. The ID must not be:
// mail, claims, space membership and every hint that names an agent are keyed
// on it, so a mutable address is a message delivered to the wrong agent.
func TestAnAgentCanRenameItselfWithoutChangingItsAddress(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	a := reg(t, s, "agent", "tok-a", t0)
	id := a.ID

	res := mustApply(t, s, &Op{
		Kind: OpUpdate, Token: a.Token,
		Name: "ledger-surgeon", Description: "repairing hash-chain gaps",
	}, t0)

	if s.Agents[id] == nil {
		t.Fatalf("the agent is no longer reachable at %q: a rename moved the address", id)
	}
	if got := s.Agents[id].Name; got != "ledger-surgeon" {
		t.Errorf("name = %q, want the new one", got)
	}
	if got := s.Agents[id].ID; got != id {
		t.Errorf("id = %q, want %q unchanged: the id is the address", got, id)
	}
	// Said out loud, because an agent that renames itself and is not told will
	// publish the new name as its address and wonder where its mail went.
	if res["address"] == nil {
		t.Error("the result does not say the id is unchanged")
	}
	if res["renamed_from"] != "agent" {
		t.Errorf("renamed_from = %v, want the old name", res["renamed_from"])
	}
}

// Renaming onto a live agent's name is refused, not suffixed.
//
// register suffixes on collision because a new agent has no history to protect.
// Here both agents already exist, and liveSiblingOf redirects a dead agent's
// mail to a same-named live one: so a rename onto somebody else's name is a
// mail-redirection primitive, not a naming preference.
func TestRenamingOntoALiveAgentsNameIsRefused(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	reg(t, s, "reviewer", "tok-r", t0)
	b := reg(t, s, "worker", "tok-w", t0)

	_, _, err := s.Apply(&Op{Kind: OpUpdate, Token: b.Token, Name: "reviewer"}, t0)
	if err == nil {
		t.Fatal("renaming onto a live peer's name was allowed")
	}
	var ce *Error
	if !errors.As(err, &ce) || ce.Code != "E_NAME_TAKEN" {
		t.Errorf("err = %v, want E_NAME_TAKEN", err)
	}
	if ce != nil && ce.Hint == "" {
		t.Error("no hint: every error carries the corrective call")
	}
}

// The self-reported identity fields merge; the description still clears.
//
// The asymmetry is deliberate and is a replay constraint, not a preference.
// Ledgers already hold `update` ops with an empty description whose recorded
// effect was to clear it, so making empty mean "leave alone" would re-fold that
// history into a different state. Name and the identity fields are new, so no
// op has them set, and merge-when-non-empty is both safe and the useful
// semantic: updating your branch must not restate your model.
func TestUpdateMergesIdentityButStillClearsDescription(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	a := reg(t, s, "worker", "tok-w", t0)
	s.Agents[a.ID].Agent = &AgentInfo{
		Harness: "Claude Code", Version: "2.1.229", Model: "claude-opus-5", Branch: "main",
	}

	mustApply(t, s, &Op{
		Kind: OpUpdate, Token: a.Token,
		Description: "still here",
		Agent:       &AgentInfo{Branch: "feat/ledger"},
	}, t0)

	got := s.Agents[a.ID].Agent
	if got.Branch != "feat/ledger" {
		t.Errorf("branch = %q, want the new one", got.Branch)
	}
	if got.Model != "claude-opus-5" {
		t.Errorf("model = %q: an unmentioned field was cleared", got.Model)
	}
	// Harness and version are the client's word rather than the model's, which
	// is the one part of an identity that is not self-description. An agent
	// must not be able to overwrite them.
	mustApply(t, s, &Op{
		Kind: OpUpdate, Token: a.Token,
		Agent: &AgentInfo{Harness: "definitely-not-claude", Version: "9.9.9"},
	}, t0)
	if got.Harness != "Claude Code" || got.Version != "2.1.229" {
		t.Errorf("harness/version = %q/%q: an agent overwrote what its client stated",
			got.Harness, got.Version)
	}

	// And an empty description still clears, because that is what every
	// already-ledgered update with an empty description did.
	mustApply(t, s, &Op{Kind: OpUpdate, Token: a.Token}, t0)
	if d := s.Agents[a.ID].Description; d != "" {
		t.Errorf("description = %q, want cleared: replay of old ops depends on it", d)
	}
}

// A name that says nothing is answered, not refused.
//
// Advisory, not coercive: a name is a label and refusing one would be coercion.
// But the cost lands on a human who is not in the conversation, and register is
// the one moment it can be said to the party that can fix it.
func TestAPlaceholderNameIsCalledOut(t *testing.T) {
	for _, name := range []string{"agent", "Claude-2", "assistant", "worker_1", "opus"} {
		if genericAgentName(name) == "" {
			t.Errorf("%q was not recognised as a placeholder", name)
		}
	}
	// Names that pick an agent out of a roster are left alone. "claude-code-linter"
	// is specific despite naming the harness; "reviewer-2" is a real second reviewer.
	for _, name := range []string{"reviewer", "reviewer-2", "ledger-surgeon", "claude-code-linter"} {
		if got := genericAgentName(name); got != "" {
			t.Errorf("%q was called a placeholder (%q); it is specific enough to address", name, got)
		}
	}

	s := NewState("n1", DefaultLimits())
	res := mustApply(t, s, &Op{Kind: OpRegister, Name: "agent", Nonce: "n-1"}, t0)
	if res["naming"] == nil {
		t.Error("registering as \"agent\" said nothing about the name")
	}
	res2 := mustApply(t, s, &Op{Kind: OpRegister, Name: "ledger-surgeon", Nonce: "n-2"}, t0)
	if res2["naming"] != nil {
		t.Errorf("a specific name was lectured about: %v", res2["naming"])
	}
}
