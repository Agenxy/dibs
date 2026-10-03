package engine

import (
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// Sending to the human is not pull-only: a person is reached by the desktop
// notification, and the warning written for agents told senders that nothing
// could wake "dibs web" and delivery waited on inbox or check_in.
func TestSendingToTheHumanIsNotCalledPullOnly(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	registered, _, err := st.Apply(&core.Op{
		Kind: core.OpRegister, Name: "maintainer", NewToken: "tok-h",
		Agent: &core.AgentInfo{Harness: "dibs web", Surface: "web"},
	}, t0Engine())
	if err != nil {
		t.Fatal("setup:", err)
	}
	// Names are labels, not addresses: registration may slug or suffix them.
	id, ok := registered["agent_id"].(string)
	if !ok || id == "" {
		t.Fatalf("setup: registration returned no agent id: %v", registered)
	}
	l := st.Agents[id]
	if l == nil {
		t.Fatalf("setup: registered agent %q is absent", id)
	}
	if e.PullOnlyNote(l) == "" {
		t.Fatal("setup: an unwakeable row with no human identity drew no note, so the " +
			"exemption below proves nothing")
	}
	e.human.mu.Lock()
	e.human.agent = l.ID
	e.human.mu.Unlock()
	if n := e.PullOnlyNote(l); n != "" {
		t.Errorf("a send to the human carried the agent wake warning: %q\n"+
			"  the sender is told nothing can reach the person the desktop is about to notify", n)
	}
}
