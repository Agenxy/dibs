package core

import (
	"encoding/json"
	"testing"
	"time"
)

func TestRecordedDeadlineWinsAfterClockReversalAtExactBoundary(t *testing.T) {
	base := time.Now()
	until := base.Add(24 * time.Hour)
	forever := time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	doneUntil := base.Add(72 * time.Hour)
	st := NewState("clock", DefaultLimits())
	type record struct {
		Op *Op
		At time.Time
	}
	var records [][]byte
	apply := func(op *Op, at time.Time) Result {
		t.Helper()
		r, _, err := st.Apply(op, at)
		if err != nil {
			t.Fatal("setup:", err)
		}
		data, err := json.Marshal(record{op, at})
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, data)
		return r
	}
	apply(&Op{Kind: OpRegister, Name: "lead", NewToken: "lead"}, base)
	apply(&Op{Kind: OpRegister, Name: "worker", NewToken: "worker"}, base)
	parent := apply(&Op{Kind: OpSendMessage, Token: "lead", To: "worker", MsgType: MsgRequest, Body: "proof"}, base)["msg_serial"].(uint64)
	apply(&Op{Kind: OpRespond, Token: "worker", MsgSerial: parent, Disposition: "approve", RetainUntil: &until}, base)
	// Done was recorded before the wall clock moved backward. Correction's
	// explicit deadline is authoritative; the old done timestamp stays intact.
	apply(&Op{Kind: OpRespond, Token: "worker", MsgSerial: parent, Disposition: "done", RetainUntil: &doneUntil}, base.Add(48*time.Hour))
	apply(&Op{Kind: OpRespond, Token: "lead", MsgSerial: parent, Disposition: "flag", Body: "correct", RetainUntil: &forever}, base)
	apply(&Op{Kind: OpRespond, Token: "worker", MsgSerial: parent, Disposition: "progress", Body: "corrected", RetainUntil: &until}, base)
	if st.Messages[parent].RetainUntil != st.Messages[parent].RetainUntil.Round(0) || st.Messages[parent].RetainUntil.Location() != time.UTC {
		t.Error("fold retained an unserialized clock or local zone in the recorded deadline")
	}
	replayed := NewState("clock", DefaultLimits())
	for _, data := range records {
		var r record
		if err := json.Unmarshal(data, &r); err != nil {
			t.Fatal(err)
		}
		if _, _, err := replayed.Apply(r.Op, r.At); err != nil {
			t.Fatal("replay:", err)
		}
	}
	for _, candidate := range []*State{st, replayed} {
		for _, delta := range []time.Duration{-time.Nanosecond, 0} {
			_, _, err := candidate.Apply(&Op{Kind: OpSweep, PurgeMail: true, V7Semantics: true, KeepOwed: true}, until.Add(delta))
			if err != nil {
				t.Fatal(err)
			}
			if (candidate.Messages[parent] != nil) != (delta < 0) {
				t.Errorf("live/replay retention disagrees with recorded deadline at delta %s", delta)
			}
		}
	}
}

func TestRecordedRetentionAndLegacyMailShareTheSameFold(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	st := NewState("retention", DefaultLimits())
	apply := func(op *Op) Result {
		t.Helper()
		r, _, err := st.Apply(op, now)
		if err != nil {
			t.Fatalf("setup %s/%s: %v", op.Kind, op.Disposition, err)
		}
		return r
	}
	apply(&Op{Kind: OpRegister, Name: "lead", NewToken: "lead"})
	apply(&Op{Kind: OpRegister, Name: "worker", NewToken: "worker"})
	old := apply(&Op{Kind: OpSendMessage, Token: "lead", To: "worker", MsgType: MsgQuestion, Body: "legacy"})["msg_serial"].(uint64)
	apply(&Op{Kind: OpRespond, Token: "worker", MsgSerial: old, Disposition: "answer"})
	parent := apply(&Op{Kind: OpSendMessage, Token: "lead", To: "worker", MsgType: MsgRequest, Body: "work", Milestones: []string{"one", "two"}})["msg_serial"].(uint64)
	day := now.Add(24 * time.Hour)
	forever := time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	apply(&Op{Kind: OpRespond, Token: "worker", MsgSerial: parent, Disposition: "approve", RetainUntil: &day})
	apply(&Op{Kind: OpRespond, Token: "worker", MsgSerial: parent, Disposition: "done", Body: "original", Deliverable: "artifact", RetainUntil: &day})
	apply(&Op{Kind: OpRespond, Token: "lead", MsgSerial: parent, Disposition: "flag", Body: "whole work", RetainUntil: &forever})
	apply(&Op{Kind: OpRespond, Token: "lead", MsgSerial: parent, Disposition: "flag", Milestone: 2, Body: "second step", RetainUntil: &forever})
	apply(&Op{Kind: OpRespond, Token: "worker", MsgSerial: parent, Disposition: "progress", Milestone: 1, Body: "partial", RetainUntil: &forever})
	apply(&Op{Kind: OpRespond, Token: "lead", MsgSerial: parent, Disposition: "accept", RetainUntil: &forever})
	m := st.Messages[parent]
	if !m.HasUnresolvedReviewFlags() || !m.HasUnresolvedReviewFlagsAfter("accept", 1) || m.HasUnresolvedReviewFlagsAfter("progress", 2) {
		t.Fatal("whole-work resolution erased another step's flag")
	}
	apply(&Op{Kind: OpRespond, Token: "lead", MsgSerial: parent, Disposition: "accept", Milestone: 2, RetainUntil: &day})
	if m.HasUnresolvedReviewFlags() || m.State != MsgStateDone || m.Response != "done: original" || m.Deliverable != "artifact" || m.Owed(now) {
		t.Fatal("review resolution changed completion or left an unresolved flag")
	}
	if _, _, err := st.Apply(&Op{Kind: OpRespond, Token: "worker", MsgSerial: parent, Disposition: "progress", Milestone: 2, RetainUntil: &forever}, now); err == nil || !m.RetainUntil.Equal(day) {
		t.Fatal("refused progress changed retention")
	}
	serial := st.Serial
	apply(&Op{Kind: OpHeartbeat, Token: "lead", RetainUntil: &forever})
	if st.Serial != serial || !m.RetainUntil.Equal(day) {
		t.Fatal("no-op changed replayable retention")
	}
	now = now.Add(20 * time.Minute)
	apply(&Op{Kind: OpSweep, PurgeMail: true, V7Semantics: true, KeepOwed: true})
	if st.Messages[old] != nil || st.Messages[parent] == nil {
		t.Fatal("mixed legacy/current retention changed the old sweep decision")
	}
	now = now.Add(25 * time.Hour)
	apply(&Op{Kind: OpSweep, PurgeMail: true, V7Semantics: true, KeepOwed: true})
	if st.Messages[parent] != nil {
		t.Fatal("resolved completed work outlived its recorded review window")
	}
}
