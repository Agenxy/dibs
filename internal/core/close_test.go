package core

import "testing"

// The sole member of a space may close it.
//
// The occupancy rule exists so nobody tidies away somebody else's working
// context, and when the only occupant IS the caller there is no somebody else.
// Reported by k7-b from a live board: close_space refused because the space had
// one member, which was them; leave_space then removed the now-empty space, so
// the close they had been told to make failed with E_NO_AGENT. The documented
// path ended in an error and the working path was undocumented.
func TestTheSoleMemberOfASpaceCanCloseIt(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	a := reg(t, s, "worker", "tok-w", t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: a.Token}, t0)
	mustApply(t, s, &Op{Kind: OpSpaceOpen, Token: a.Token, Space: "issue-9", Text: "work"}, t0)
	if got := len(s.Spaces["issue-9"].Members); got != 1 {
		t.Fatalf("setup: %d member(s), want the opener alone", got)
	}

	if _, _, err := s.Apply(&Op{
		Kind: OpSpaceClose, Token: a.Token, Space: "issue-9", Note: "done",
	}, t0); err != nil {
		t.Fatalf("the sole member could not close its own space: %v", err)
	}

	// Somebody else's space is still protected: that is what the rule is for.
	b := reg(t, s, "peer", "tok-p", t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: b.Token}, t0)
	mustApply(t, s, &Op{Kind: OpSpaceOpen, Token: b.Token, Space: "issue-10", Text: "theirs"}, t0)
	mustApply(t, s, &Op{Kind: OpSpaceJoin, Token: a.Token, Space: "issue-10"}, t0)
	if _, _, err := s.Apply(&Op{
		Kind: OpSpaceClose, Token: b.Token, Space: "issue-10",
	}, t0); err == nil {
		t.Error("a space with another agent in it was closed: that agent's working " +
			"context was tidied away by somebody else")
	}
}
