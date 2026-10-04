package core

import "testing"

func TestReadFieldsAreIngressOnlyAndActorScoped(t *testing.T) {
	s, parent := taskRequest(t, "proof")
	cutoff := s.Serial + 1
	mustApply(t, s, &Op{Kind: OpInitializeReviewRead, ReviewReadCutoff: cutoff}, t0)
	mustApply(t, s, &Op{
		Kind: OpRespond, Token: "tw", MsgSerial: parent,
		Disposition: "progress", Milestone: 1, Body: "proof",
	}, t0)
	mustApply(t, s, &Op{
		Kind: OpRespond, Token: "tl", MsgSerial: parent,
		Disposition: "flag", Milestone: 1, Body: "fix",
	}, t0)
	reg(t, s, "stranger", "ts", t0)
	for _, op := range []*Op{
		{Kind: OpAckBoard, Token: "tl", OutcomeThroughSerial: 1},
		{Kind: OpOutcomeRead, Token: "tl", ReviewReadCutoff: 1},
		{Kind: OpInitializeReviewRead, ReviewReadCutoff: s.Serial},
		{Kind: OpInitializeReviewRead, ReviewReadCutoff: s.Serial + 1, OutcomeThroughSerial: 1},
		{Kind: OpOutcomeRead, Token: "tw", MsgSerial: parent, OutcomeThroughSerial: s.Serial + 1},
	} {
		before := s.Serial
		if err := s.Admit(op); codeOf(err) != "E_BAD_ARG" {
			t.Errorf("invalid read field admitted: %+v: %v", op, err)
		}
		if s.Serial != before {
			t.Fatal("Admit mutated state")
		}
	}
	if err := s.Admit(&Op{
		Kind: OpOutcomeRead, Token: "ts", MsgSerial: parent,
		OutcomeThroughSerial: s.Serial,
	}); codeOf(err) != "E_NO_MESSAGE" {
		t.Errorf("stranger learned a predecessor's outcome: %v", err)
	}
	// Historical full reads retain their old shape and fold semantics.
	mustApply(t, s, &Op{Kind: OpOutcomeRead, Token: "tl", MsgSerial: parent}, t0)
	if s.Messages[parent].ReviewReadAt != 0 {
		t.Fatal("sender full read consumed the worker's independent review view")
	}
}

func TestRecipientReviewPrefixIsIndependentAndIdempotent(t *testing.T) {
	s, parent := taskRequest(t, "proof")
	mustApply(t, s, &Op{Kind: OpInitializeReviewRead, ReviewReadCutoff: s.Serial + 1}, t0)
	mustApply(t, s, &Op{
		Kind: OpRespond, Token: "tw", MsgSerial: parent,
		Disposition: "progress", Milestone: 1, Body: "proof",
	}, t0)
	mustApply(t, s, &Op{
		Kind: OpRespond, Token: "tl", MsgSerial: parent,
		Disposition: "flag", Milestone: 1, Body: "fix",
	}, t0)
	m := s.Messages[parent]
	op := &Op{Kind: OpOutcomeRead, Token: "tw", MsgSerial: parent, OutcomeThroughSerial: m.LatestReviewSerial()}
	if err := s.Admit(op); err != nil {
		t.Fatal(err)
	}
	mustApply(t, s, op, t0)
	if m.ReviewReadAt != m.LatestReviewSerial() || m.OutcomeReadAt != 0 {
		t.Fatal("review prefix changed sender view or did not reach recipient view")
	}
	before := s.Serial
	mustApply(t, s, op, t0)
	if s.Serial != before {
		t.Fatal("second recipient read advanced serial")
	}
	mustApply(t, s, &Op{
		Kind: OpRespond, Token: "tl", MsgSerial: parent,
		Disposition: "flag", Milestone: 1, Body: "later fix",
	}, t0)
	if m.ReviewReadAt >= m.LatestReviewSerial() {
		t.Fatal("new review was pre-read")
	}
}

func TestHistoricalZeroPrefixReadKeepsItsOriginalFold(t *testing.T) {
	s, parent := taskRequest(t, "proof")
	op := &Op{Kind: OpOutcomeRead, Token: "tl", MsgSerial: parent}
	mustApply(t, s, op, t0)
	read := s.Messages[parent].OutcomeReadAt
	mustApply(t, s, &Op{
		Kind: OpRespond, Token: "tw", MsgSerial: parent,
		Disposition: "progress", Milestone: 1, Body: "later report",
	}, t0)
	before := s.Serial
	mustApply(t, s, op, t0)
	if s.Serial != before || s.Messages[parent].OutcomeReadAt != read {
		t.Fatal("historical read op acquired new prefix behavior")
	}
	// The new additive field, not an old ack/read op, opts into that behavior.
	op.OutcomeThroughSerial = s.Messages[parent].LatestOutcomeSerial()
	mustApply(t, s, op, t0)
	if s.Serial != before+1 || s.Messages[parent].OutcomeReadAt != op.OutcomeThroughSerial {
		t.Fatal("new prefix was not read")
	}
}
