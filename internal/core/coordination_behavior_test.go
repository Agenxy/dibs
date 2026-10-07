package core

import (
	"strconv"
	"testing"
)

func TestDeclarationClassifiesOnlyProvenCoordination(t *testing.T) {
	for _, tc := range []struct {
		name, kind, from, refKind string
		mixed, coordinated        bool
	}{
		{name: "request parties", kind: MsgRequest, from: "alpha", refKind: MsgRequest, coordinated: true},
		{name: "reverse question parties", kind: MsgQuestion, from: "bravo", refKind: MsgQuestion, coordinated: true},
		{name: "wrong message kind", kind: MsgRequest, from: "alpha", refKind: MsgQuestion},
		{name: "third party sender", kind: MsgRequest, from: "third", refKind: MsgRequest},
		{name: "unexplained shared objective", kind: MsgRequest, from: "alpha", refKind: MsgRequest, mixed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewState("coordination", DefaultLimits())
			for _, id := range []string{"alpha", "bravo", "third"} {
				ackReg(t, s, id, id, t0)
			}
			to := "bravo"
			if tc.from == "bravo" {
				to = "alpha"
			}
			mail := mustApply(t, s, &Op{
				Kind: OpSendMessage, Token: tc.from, To: to, MsgType: tc.kind, Body: "coordinate",
			}, t0)["msg_serial"].(uint64)
			refs := []string{tc.refKind + ":" + strconv.FormatUint(mail, 10)}
			if tc.mixed {
				refs = append(refs, "issue:unrelated")
			}
			mustApply(t, s, &Op{Kind: OpSetSlot, Token: "alpha", Text: "work", Activity: "implement", Refs: refs}, t0)
			op := &Op{Kind: OpSetSlot, Token: "bravo", Text: "work", Activity: "implement", Refs: refs}
			if err := s.Admit(op); err != nil {
				t.Fatal("setup: declaration was not admitted:", err)
			}
			res := mustApply(t, s, op, t0)
			overlaps, ok := res["overlaps"].([]SlotOverlap)
			if !ok || len(overlaps) != 1 {
				t.Fatalf("setup: expected the actual declaration overlap, got %v", res)
			}
			want := SignalSameObjective
			if tc.coordinated {
				want = SignalCoordination
			}
			if overlaps[0].Signal != want || overlaps[0].Complementary != tc.coordinated {
				t.Fatalf("relationship classified as %v; want %s", overlaps[0], want)
			}
			if (res["warning"] == nil) != tc.coordinated {
				t.Fatalf("warning disagrees with proven relationship: %v", res)
			}
		})
	}
}

func TestSharedWaitEvidenceRequiresEverySharedRef(t *testing.T) {
	for _, tc := range []struct {
		name       string
		a, b       Slot
		complement bool
	}{
		{"shared request wait", waitingEvidenceSlot("ci", "request:42"), waitingEvidenceSlot("review", "request:42"), true},
		{"question wait", waitingEvidenceSlot("ci", "question:42"), waitingEvidenceSlot("review", "question:42"), true},
		{"one side waiting", waitingEvidenceSlot("ci", "request:42"), waitingEvidenceSlot("", "request:42"), false},
		{"no shared ref", waitingEvidenceSlot("ci", "request:42"), waitingEvidenceSlot("ci", "request:43"), false},
		{
			"mixed shared objective",
			waitingEvidenceSlot("ci", "request:42", "issue:7"), waitingEvidenceSlot("ci", "request:42", "issue:7"), false,
		},
		{"zero serial", waitingEvidenceSlot("ci", "request:0"), waitingEvidenceSlot("ci", "request:0"), false},
		{
			"overflow serial", waitingEvidenceSlot("ci", "question:18446744073709551616"),
			waitingEvidenceSlot("ci", "question:18446744073709551616"), false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := EvidenceBetween(tc.a, tc.b, "", "", "", nil, nil).Complementary; got != tc.complement {
				t.Fatalf("shared waiting evidence = %t; want %t", got, tc.complement)
			}
		})
	}
}

func waitingEvidenceSlot(waiting string, refs ...string) Slot {
	return Slot{Waiting: waiting, Refs: refs}
}
