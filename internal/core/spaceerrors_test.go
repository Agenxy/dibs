package core

import (
	"errors"
	"strings"
	"testing"
)

// A missing SPACE and a missing AGENT are different facts, and the error must
// say which one it is.
//
// THE BUG THIS CATCHES. Nine call sites keyed on a space id answered
// E_NO_AGENT, and one of the hints read "open_space it, or list agents first".
// So an agent that asked to join a space nobody had opened was told no such
// AGENT exists and pointed at the roster, where it would look for something
// that was never going to be there. Dibs holds that "every error carries a hint
// that tells a drifted agent the corrective call"; these named the wrong noun
// and then named the wrong call.
//
// It is the vocabulary rename again, at its most expensive: the sweep could not
// tell the participant from the work it joins, and the error surface is where
// that costs an agent a turn rather than a reader a frown.
//
// Pinned in BOTH directions on purpose. Repairing one side by making everything
// answer E_NO_SPACE would be just as wrong and would pass a one-sided test,
// which is the repair somebody reaches for first.
func TestAMissingSpaceIsNotAMissingAgent(t *testing.T) {
	spaceOps := []struct {
		name string
		op   *Op
	}{
		{"join", &Op{Kind: OpSpaceJoin, Space: "ghost"}},
		{"leave", &Op{Kind: OpSpaceLeave, Space: "ghost"}},
		{"post", &Op{Kind: OpSpacePost, Space: "ghost", Text: "hello"}},
		{"watch", &Op{Kind: OpSpaceSubscribe, Space: "ghost"}},
		{"retitle", &Op{Kind: OpSpaceRetitle, Space: "ghost", Text: "t"}},
	}
	for _, tc := range spaceOps {
		t.Run(tc.name, func(t *testing.T) {
			s, a := chState(t, "asker")
			tc.op.Token = a["asker"].Token
			err := mustFail(t, s, tc.op)

			var ce *Error
			if !errors.As(err, &ce) {
				t.Fatalf("not a coded error: %v", err)
			}
			if ce.Code != "E_NO_SPACE" {
				t.Errorf("%s on a space that does not exist answered %s, want E_NO_SPACE. "+
					"An agent told \"no agent ghost\" goes looking on the roster for "+
					"something that was never an agent", tc.op.Kind, ce.Code)
			}
			// The hint is the half that actually redirects, so it is asserted
			// rather than assumed: a corrected code with a hint still saying
			// "list agents" leaves the agent exactly as lost.
			if strings.Contains(ce.Hint, "list agents") {
				t.Errorf("%s hints %q, which sends the agent to the roster for a space",
					tc.op.Kind, ce.Hint)
			}
		})
	}

	// And the other side still holds: a missing AGENT is E_NO_AGENT.
	t.Run("a missing agent is unchanged", func(t *testing.T) {
		s, a := chState(t, "director", "other")
		do(t, s, &Op{Kind: OpSpaceOpen, Token: a["director"].Token, Space: "work", Text: "t"})
		makeCoordinator(t, s, "director")
		err := mustFail(t, s, &Op{
			Kind: OpSpaceAdmit, Token: a["director"].Token, Space: "work", To: "ghost",
		})
		var ce *Error
		if !errors.As(err, &ce) || ce.Code != "E_NO_AGENT" {
			t.Fatalf("admitting an agent that does not exist must stay E_NO_AGENT, got %v", err)
		}
	})
}
