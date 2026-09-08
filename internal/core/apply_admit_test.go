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
// A LIST, not a sweep, and that is worth being honest about: this said it
// "walks every op kind Admit can reject" and it does not. It walks the ones
// written below. A pre-release review made exactly that point after finding two
// new bounds added to Apply that this could not see, so the claim was doing
// harm: a reader who believed it had no reason to check by hand.
//
// Every entry must be added when a rule is added to Admit. There is no
// enumeration of Admit's rejections to iterate, short of fuzzing every field of
// every op kind, and a list somebody must remember is still better than the
// nothing that preceded it.
func TestApplyFoldsWhateverAdmitRejects(t *testing.T) {
	long := func(n int) string { return strings.Repeat("x", n+1) }
	lim := DefaultLimits()
	rejected := []*Op{
		{Kind: OpSpaceAnnounce, Body: ""},
		{Kind: OpSpaceAnnounce, Body: "   "},
		{Kind: OpSpacePost, Body: ""},
		{Kind: OpSpacePost, Body: "\n\t "},
		// Added after both were found in the fold: an agent renaming itself, and
		// a space being retitled. Neither could break replay yet, because the
		// limits have not moved; the point is that they COULD the moment a later
		// build lowers one.
		{Kind: OpUpdate, Name: long(lim.MaxNameBytes)},
		{Kind: OpUpdate, Description: long(lim.MaxDescBytes)},
		{Kind: OpSpaceRetitle, Text: long(lim.MaxNameBytes)},
		// bind_session repeated Admit's size bound inside the fold, so replay of
		// an op accepted under a larger limit would have been refused by a
		// daemon running a smaller one. The list did not know this op existed,
		// which is the standing weakness this test's own comment admits to:
		// every entry has to be added by hand when a rule is added.
		{Kind: OpBindSession, SessionID: long(lim.MaxNameBytes)},
		// grant_role checked its own vocabulary inside the fold while the typed
		// request path beside it did the same job in Admit and carried the
		// paragraph saying why. The list did not know about this op either,
		// which is this test's standing weakness: every entry is added by hand.
		// A role removed or renamed by a later build would have made that build
		// refuse a grant_role already in its own ledger.
		{Kind: OpGrantRole, To: "speaker", Mode: "superuser"},
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
		mustApply(t, s, &Op{Kind: OpRegister, Name: "speaker", NewToken: "tok"}, now)
		mustApply(t, s, &Op{Kind: OpAckBoard, Token: "tok"}, now)
		mustApply(t, s, &Op{Kind: OpSpaceOpen, Token: "tok", Space: "work", Text: "w"}, now)

		op := *proto
		op.Token, op.Space = "tok", "work"
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
		{Kind: OpSpaceAnnounce, Body: "freezing auth/retry.go until Friday"},
		{Kind: OpSpacePost, Body: "picked this up"},
		// A single character is a real message; only nothing-at-all is not.
		{Kind: OpSpaceAnnounce, Body: "?"},
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
	err := Admit(&Op{Kind: OpSpaceAnnounce, Body: ""}, DefaultLimits())
	if err == nil || !strings.Contains(err.Error(), "body") {
		t.Errorf("the error must name `body`, which is the mistake it exists to catch: %v", err)
	}
}
