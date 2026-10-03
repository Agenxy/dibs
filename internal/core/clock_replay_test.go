package core

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"
)

// shiftedWall simulates sleep or a clock correction without changing the host
// clock. Add cannot do this: it advances BOTH clocks. Fail loudly if Go changes
// its Time layout, then check the public clock operations before using it.
func shiftedWall(t *testing.T, base time.Time, shift time.Duration) time.Time {
	t.Helper()
	typ := reflect.TypeFor[time.Time]()
	f, ok := typ.FieldByName("wall")
	if !ok || f.Type.Kind() != reflect.Uint64 || f.Offset != 0 || base == base.Round(0) {
		t.Fatal("clock fixture requires Go's packed wall field and a monotonic-bearing Time")
	}
	if shift%time.Second != 0 {
		t.Fatal("clock fixture shift must be whole seconds")
	}
	want := base.Round(0).Add(shift)
	out := base
	wall := (*uint64)(unsafe.Pointer(&out))
	const secondsMask = uint64(1<<33 - 1)
	seconds := int64((*wall >> 30) & secondsMask)
	seconds += int64(shift / time.Second)
	if seconds < 0 || seconds > int64(secondsMask) {
		t.Fatal("clock fixture wall seconds out of range")
	}
	*wall = (*wall & ^(secondsMask << 30)) | uint64(seconds)<<30
	if !out.Round(0).Equal(want) || out.Sub(base) != 0 || out.Round(0).Sub(base.Round(0)) != shift {
		t.Fatal("clock fixture failed to move wall time independently of monotonic time")
	}
	return out
}

// Enter through Apply, the production mutation door, and JSON-roundtrip the
// recorded op/time as the encrypted ledger does. Compare the WHOLE state after
// every step, not only the clock field or the expiry helper.
func TestApplyUsesTheClockTheLedgerRecords(t *testing.T) {
	for _, tc := range []struct {
		name  string
		awake time.Duration
		shift time.Duration
	}{
		{"sleep", 2 * time.Minute, 2 * time.Hour},
		{"owed_and_consumed_retention", 2 * time.Minute, 26 * time.Hour},
		{"backward_correction", 2 * time.Hour, -2 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			live := NewState("clock", DefaultLimits())
			fold := NewState("clock", DefaultLimits())
			apply := func(op *Op, now time.Time) Result {
				t.Helper()
				res, _, err := live.Apply(op, now)
				if err != nil {
					t.Fatalf("live %s: %v", op.Kind, err)
				}
				wire, err := json.Marshal(struct {
					Op  *Op
					Now time.Time
				}{op, now})
				if err != nil {
					t.Fatal(err)
				}
				var recorded struct {
					Op  *Op
					Now time.Time
				}
				if err := json.Unmarshal(wire, &recorded); err != nil {
					t.Fatal(err)
				}
				if _, _, err := fold.Apply(recorded.Op, recorded.Now); err != nil {
					t.Fatalf("fold %s: %v", op.Kind, err)
				}
				a, err := json.Marshal(live)
				if err != nil {
					t.Fatal(err)
				}
				b, err := json.Marshal(fold)
				if err != nil {
					t.Fatal(err)
				}
				if string(a) != string(b) {
					t.Fatalf("live state != fold after %s at wall %s", op.Kind, now.Round(0))
				}
				return res
			}
			for _, name := range []string{"sender", "recipient"} {
				apply(&Op{Kind: OpRegister, Name: name, NewToken: name}, start)
				apply(&Op{Kind: OpAckBoard, Token: name}, start)
				if got := live.Agents[name].LastCoordination; got != got.Round(0) {
					t.Fatal("Apply stored a process-local clock the ledger cannot reproduce")
				}
			}
			q := apply(&Op{Kind: OpSendMessage, Token: "sender", To: "recipient", MsgType: MsgQuestion, Body: "question", DeadlineSec: 3600}, start)["msg_serial"].(uint64)
			r := apply(&Op{Kind: OpSendMessage, Token: "sender", To: "recipient", MsgType: MsgRequest, Body: "work", DeadlineSec: 3600}, start)["msg_serial"].(uint64)
			apply(&Op{Kind: OpRespond, Token: "recipient", MsgSerial: r, Disposition: "approve"}, start)
			apply(&Op{Kind: OpClaim, Token: "recipient", Path: "/clock-fixture", Mode: ClaimExclusive}, start)
			now := shiftedWall(t, start.Add(tc.awake), tc.shift)
			apply(&Op{Kind: OpSweep, PurgeMail: true, V7Semantics: true, KeepOwed: true}, now)
			if got := live.Messages[q].State; (got == MsgStateExpiredSilent) != (now.Round(0).Sub(start.Round(0)) >= time.Hour) {
				t.Fatalf("question expiry follows the wrong clock: %s", got)
			}
			if len(live.Claims) != len(fold.Claims) || (live.Messages[r] != nil) != (fold.Messages[r] != nil) {
				t.Fatal("claim or owed retention disagrees with fold")
			}
			age := now.Round(0).Sub(start.Round(0))
			if (len(live.Claims) == 0) != (age > live.Limits.ClaimLease) {
				t.Fatal("claim expiry did not use recorded wall age")
			}
			if (live.Messages[r] != nil) != (age <= ObligationWindow) {
				t.Fatal("approved-request retention did not use recorded wall age")
			}
		})
	}
}

func TestHistoricalLateQuestionAnswerReplays(t *testing.T) {
	s := NewState("late-answer", DefaultLimits())
	ackReg(t, s, "sender", "sender", t0)
	ackReg(t, s, "recipient", "recipient", t0)
	q := mustApply(t, s, &Op{Kind: OpSendMessage, Token: "sender", To: "recipient", MsgType: MsgQuestion, Body: "question", DeadlineSec: 3600}, t0)["msg_serial"].(uint64)
	mustApply(t, s, &Op{Kind: OpSweep}, t0.Add(2*time.Hour))
	if m := s.Messages[q]; m.State != MsgStateExpiredSilent || m.ExpireDetail == "" {
		t.Fatal("setup: question did not expire with an active recipient")
	}
	// The sender may have read the expiry verdict before the answer arrives.
	mustApply(t, s, &Op{Kind: OpOutcomeRead, Token: "sender", MsgSerial: q}, t0.Add(2*time.Hour))
	for _, bad := range []*Op{
		{Kind: OpRespond, Token: "sender", MsgSerial: q, Disposition: "answer"},
		{Kind: OpRespond, Token: "recipient", MsgSerial: q, Disposition: "answer", Body: strings.Repeat("x", s.Limits.MaxBodyBytes+1)},
	} {
		before, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.Apply(bad, t0.Add(2*time.Hour)); err == nil {
			t.Fatal("late answer bypassed recipient authorization or body limit")
		}
		after, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Fatal("refused late answer mutated its expiry verdict")
		}
	}
	before := s.Serial
	res, evs, err := s.Apply(&Op{Kind: OpRespond, AgentID: "recipient", MsgSerial: q, Disposition: "answer", Body: "late answer"}, t0.Add(2*time.Hour+time.Minute))
	if err != nil {
		t.Fatalf("historically accepted answer must fold: %v", err)
	}
	m := s.Messages[q]
	if res["state"] != MsgStateAnswered || m.State != MsgStateAnswered || m.Response != "late answer" || m.ExpireDetail != "" || m.OutcomeReadAt != 0 || !m.Consumed || m.RespondedAt != before+1 || s.Serial != before+1 || len(evs) != 1 || evs[0].Type != "message.answered" {
		t.Fatal("late answer did not replace the expiry verdict in exactly one ledger transition")
	}
	if _, _, err := s.Apply(&Op{Kind: OpRespond, Token: "recipient", MsgSerial: q, Disposition: "answer", Body: "replacement"}, t0.Add(3*time.Hour)); err == nil {
		t.Fatal("late-answer repair allowed rewriting an answer")
	}
}

func TestLateAnswerDoesNotReopenRequestsOrOtherVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		typ   string
		state string
		disp  string
	}{
		{"request_approval", MsgRequest, MsgStateExpiredSilent, "approve"},
		{"request_answer", MsgRequest, MsgStateExpiredSilent, "answer"},
		{"declined_question", MsgQuestion, MsgStateDeclined, "answer"},
		{"answered_question", MsgQuestion, MsgStateAnswered, "answer"},
		{"dormant_question", MsgQuestion, MsgStateExpiredDormant, "answer"},
		{"dead_question", MsgQuestion, MsgStateExpiredDead, "answer"},
		{"decline_expired", MsgQuestion, MsgStateExpiredSilent, "decline"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewState("narrow", DefaultLimits())
			ackReg(t, s, "sender", "sender", t0)
			ackReg(t, s, "recipient", "recipient", t0)
			q := mustApply(t, s, &Op{Kind: OpSendMessage, Token: "sender", To: "recipient", MsgType: tc.typ, Body: "?", DeadlineSec: 3600}, t0)["msg_serial"].(uint64)
			s.Messages[q].State = tc.state
			before, err := json.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.Apply(&Op{Kind: OpRespond, Token: "recipient", MsgSerial: q, Disposition: tc.disp}, t0); err == nil {
				t.Fatal("repair widened a terminal refusal beyond an active-recipient question answer")
			}
			after, err := json.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("refused response mutated state")
			}
		})
	}
}
