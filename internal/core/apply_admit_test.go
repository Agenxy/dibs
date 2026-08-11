package core

import (
	"strings"
	"testing"
	"time"
)

// Apply is the FOLD. Whatever it has ever accepted, it must accept forever.
//
// This exists because a validation rule was added to Apply: announcements must
// not be empty, and Apply is what replays the ledger. A daemon holding
// announcements that were legal when written then refused to start: "replay
// apply serial 12: E_EMPTY_BODY", rejecting data it had itself written, fsynced
// and acknowledged to a caller. The state was not corrupt; the reader had
// changed its mind about the past.
//
// So the rule is structural, not a matter of care: restrictions on what callers
// may DO live in Admit, which runs only at ingress. Apply keeps folding.
//
// This test is the general form. It walks every op kind Admit can reject and
// asserts Apply still folds it, so the next rule added in the wrong place fails
// here rather than at somebody's next daemon restart.
func TestApplyFoldsWhateverAdmitRejects(t *testing.T) {
	rejected := []*Op{
		{Kind: OpLaneAnnounce, Body: ""},
		{Kind: OpLaneAnnounce, Body: "   "},
		{Kind: OpLanePost, Body: ""},
		{Kind: OpLanePost, Body: "\n\t "},
	}

	for _, proto := range rejected {
		if err := Admit(proto, DefaultLimits()); err == nil {
			t.Errorf("%s with body %q: expected Admit to reject it: if this rule "+
				"was removed, remove it from this list too", proto.Kind, proto.Body)
			continue
		}

		// Same op, replayed out of a ledger written before the rule existed.
		s := NewState("replay", DefaultLimits())
		now := time.Unix(1700000000, 0)
		mustApply(t, s, &Op{Kind: OpRegisterLane, Name: "speaker", NewToken: "tok"}, now)
		mustApply(t, s, &Op{Kind: OpAckBoard, Token: "tok"}, now)
		mustApply(t, s, &Op{Kind: OpLaneOpen, Token: "tok", Channel: "work", Text: "w"}, now)

		op := *proto
		op.Token, op.Channel = "tok", "work"
		if _, _, err := s.Apply(&op, now); err != nil {
			t.Errorf("Apply refused to fold a %s that Admit rejects: %v\n"+
				"  A ledger written before the rule existed now cannot be replayed, and the "+
				"daemon will not start. Move the check into Admit.", op.Kind, err)
		}
	}
}

// And the converse: Admit must not reject what agents legitimately do, or the
// tool becomes unusable in the name of tidiness.
func TestAdmitPassesOrdinaryTraffic(t *testing.T) {
	for _, op := range []*Op{
		{Kind: OpLaneAnnounce, Body: "freezing auth/retry.go until Friday"},
		{Kind: OpLanePost, Body: "picked this up"},
		// A single character is a real message; only nothing-at-all is not.
		{Kind: OpLaneAnnounce, Body: "?"},
		// Every other kind passes untouched.
		{Kind: OpAckBoard},
		{Kind: OpClaim, Path: "/x", Mode: "exclusive"},
		{Kind: OpSendMessage, Body: ""},
	} {
		if err := Admit(op, DefaultLimits()); err != nil {
			t.Errorf("%s with body %q was refused at ingress: %v", op.Kind, op.Body, err)
		}
	}

	// The error has to say which parameter, because the fault that produced
	// this rule was a body sent under the wrong key.
	err := Admit(&Op{Kind: OpLaneAnnounce, Body: ""}, DefaultLimits())
	if err == nil || !strings.Contains(err.Error(), "body") {
		t.Errorf("the error must name `body`, which is the mistake it exists to catch: %v", err)
	}
}
