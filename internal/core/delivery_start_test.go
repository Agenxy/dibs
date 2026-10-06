package core

import (
	"strings"
	"testing"
	"time"
)

func TestNewQuestionStartsItsClockAtRecipientAwareness(t *testing.T) {
	s := NewState("delivery-start", DefaultLimits())
	base := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	ackReg(t, s, "sender", "sender", base)
	ackReg(t, s, "recipient", "recipient", base)
	sent := mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "sender", To: "recipient", MsgType: MsgQuestion,
		Body: "question", DeadlineSec: 60, DeliveryStart: true,
	}, base)
	if note, _ := sent["deadline_note"].(string); !strings.Contains(note, "does not pause") {
		t.Fatalf("send result hides the read-then-close limitation: %v", sent)
	}
	serial := sent["msg_serial"].(uint64)
	m := s.Messages[serial]
	if !m.Deadline.IsZero() || m.ResponseWindowSec != 60 || m.NeverDeliveredAt.IsZero() {
		t.Fatalf("new question already runs before delivery: %+v", m)
	}
	mustApply(t, s, &Op{Kind: OpSweep}, base.Add(2*time.Minute))
	if m.State != MsgStatePending {
		t.Fatalf("unread question expired: %s", m.State)
	}
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "recipient"}, base.Add(2*time.Minute))
	if m.State != MsgStateDelivered || !m.DeliveredTime.Equal(base.Add(2*time.Minute)) ||
		!m.Deadline.Equal(base.Add(3*time.Minute)) {
		t.Fatalf("check_in did not start the response clock: %+v", m)
	}
	mustApply(t, s, &Op{Kind: OpSweep}, base.Add(3*time.Minute))
	if m.State != MsgStateExpiredSilent {
		t.Fatalf("delivered question failed to expire after its response window: %s", m.State)
	}
}

func TestNewQuestionHasAnHonestNeverDeliveredCeiling(t *testing.T) {
	s := NewState("never-delivered", DefaultLimits())
	base := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	ackReg(t, s, "sender", "sender", base)
	ackReg(t, s, "recipient", "recipient", base)
	serial := mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "sender", To: "recipient", MsgType: MsgRequest,
		Body: "work", DeadlineSec: 60, DeliveryStart: true,
	}, base)["msg_serial"].(uint64)
	m := s.Messages[serial]
	mustApply(t, s, &Op{Kind: OpSweep}, base.Add(7*24*time.Hour-time.Second))
	if m.State != MsgStatePending {
		t.Fatalf("never-delivered ceiling fired early: %s", m.State)
	}
	_, evs, err := s.Apply(&Op{Kind: OpSweep}, base.Add(7*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if m.State != MsgStateExpiredSilent || !strings.Contains(m.ExpireDetail, "never delivered") {
		t.Fatalf("never-delivered expiry lacks its real diagnosis: %+v", m)
	}
	found := false
	for _, ev := range evs {
		if ev.Data["msg_serial"] == serial && ev.To == "sender" && ev.Data["never_delivered"] == true {
			found = true
		}
	}
	if !found {
		t.Fatal("never-delivered expiry did not emit a sender-visible notice event")
	}
}

func TestHistoricalSendDeadlineStillStartsAtSend(t *testing.T) {
	s := NewState("historical-deadline", DefaultLimits())
	base := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	ackReg(t, s, "sender", "sender", base)
	ackReg(t, s, "recipient", "recipient", base)
	serial := mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "sender", To: "recipient", MsgType: MsgQuestion,
		Body: "old question", DeadlineSec: 60,
	}, base)["msg_serial"].(uint64)
	m := s.Messages[serial]
	if !m.Deadline.Equal(base.Add(time.Minute)) || m.ResponseWindowSec != 0 || !m.NeverDeliveredAt.IsZero() {
		t.Fatalf("historical send changed: %+v", m)
	}
}

func TestEveryDirectRecipientAwarenessStartsTheNewClock(t *testing.T) {
	base := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		op   func(uint64) *Op
	}{
		{"inbox_or_read_mail", func(serial uint64) *Op {
			return &Op{Kind: OpMarkDelivered, MsgSerials: []uint64{serial}}
		}},
		{"ack", func(serial uint64) *Op {
			return &Op{Kind: OpAckMessage, Token: "recipient", MsgSerial: serial}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewState("direct-awareness", DefaultLimits())
			ackReg(t, s, "sender", "sender", base)
			ackReg(t, s, "recipient", "recipient", base)
			serial := mustApply(t, s, &Op{
				Kind: OpSendMessage, Token: "sender", To: "recipient",
				MsgType: MsgQuestion, Body: "question", DeadlineSec: 60, DeliveryStart: true,
			}, base)["msg_serial"].(uint64)
			mustApply(t, s, tc.op(serial), base.Add(2*time.Minute))
			m := s.Messages[serial]
			if !m.DeliveredTime.Equal(base.Add(2*time.Minute)) || !m.Deadline.Equal(base.Add(3*time.Minute)) {
				t.Fatalf("direct awareness did not start clock: %+v", m)
			}
		})
	}
}
