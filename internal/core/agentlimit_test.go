package core

import (
	"errors"
	"strings"
	"testing"
)

// The refusal names the ceiling that was actually hit, and its number.
//
// Both cases returned one static "maximum number of agents reached", which
// sends the reader to the wrong limit whenever it is the persistent one.
// Measured on a live board holding 16 agents of a possible 64, where
// registration was refused because all 16 were persistent and that cap is 16.
// An operator reading "maximum number of agents" against a quarter-full board
// has been told something true and useless, and this project's honesty rule is
// that an error names the corrective call.
func TestTheAgentLimitSaysWhichCeilingAndWhatToDo(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxPersistentAgents = 2
	s := NewState("n1", lim)
	reg(t, s, "one", "t1", t0)
	reg(t, s, "two", "t2", t0)
	for _, id := range []string{"one", "two"} {
		s.Agents[id].Kind = KindPersistent
	}

	_, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "three", NewToken: "t3",
		AgentKind: KindPersistent, Nonce: "n3",
	}, t0)
	if err == nil {
		t.Fatal("setup: the persistent cap did not bite, so this proves nothing")
	}
	msg := err.Error()
	if !strings.Contains(msg, "persistent") {
		t.Errorf("the refusal does not say it is the PERSISTENT ceiling, so the "+
			"reader checks max_agents and finds room: %s", msg)
	}
	if !strings.Contains(msg, "2") {
		t.Errorf("the refusal does not state the limit, so nobody can tell whether "+
			"it is reasonable: %s", msg)
	}
	var de *Error
	if errors.As(err, &de) {
		for _, want := range []string{"adopt_agent", "ephemeral"} {
			if !strings.Contains(de.Hint, want) {
				t.Errorf("the hint omits %q: a full persistent board usually means "+
					"siblings accumulated, and the fix is to reclaim them rather than "+
					"to raise anything. Hint: %s", want, de.Hint)
			}
		}
	}
}
