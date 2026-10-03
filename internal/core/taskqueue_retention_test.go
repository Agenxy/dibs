package core

import (
	"testing"
	"time"
)

func TestRecordedQueueDebtSurvivesSweepBeyondHistoricalWindow(t *testing.T) {
	s := NewState("test", DefaultLimits())
	regPersistent(t, s, "lead", "tl", "lead-nonce", t0)
	regPersistent(t, s, "worker", "tw", "worker-nonce", t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "tw"}, t0)
	mail := mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "tl", To: "worker", MsgType: MsgRequest, Body: "future work",
	}, t0)
	n := mail["msg_serial"].(uint64)
	mustApply(t, s, &Op{Kind: OpRespond, Token: "tw", MsgSerial: n, Disposition: "queue", QueueDebt: true}, t0)
	later := t0.Add(ObligationWindow + time.Hour)
	mustApply(t, s, &Op{Kind: OpSweep, KeepOwed: false}, later)
	m := s.Messages[n]
	if m == nil || !m.Owed(later) || m.State != MsgStateQueued {
		t.Fatal("recorded queued debt vanished after the old obligation window")
	}
	mustApply(t, s, &Op{Kind: OpRespond, Token: "tw", MsgSerial: n, Disposition: "approve", QueueDebt: true}, later)
	if !m.Owed(later.Add(ObligationWindow + time.Hour)) {
		t.Fatal("starting durable debt reverted it to historical expiry")
	}
	old, oldSerial := approvedRequest(t)
	if old.Messages[oldSerial].Owed(later) {
		t.Fatal("unmarked historical approval acquired new durable semantics")
	}
}

func TestAcceptedQueueCapacityNeverEvictsOwedDebt(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxMailboxDepth = 2
	s := NewState("test", limits)
	reg(t, s, "lead", "tl", t0)
	reg(t, s, "worker", "tw", t0)
	var queued []uint64
	for range 2 {
		mail := mustApply(t, s, &Op{
			Kind: OpSendMessage, Token: "tl", To: "worker", MsgType: MsgRequest, Body: "later", QueueDebt: true,
		}, t0)
		n := mail["msg_serial"].(uint64)
		mustApply(t, s, &Op{Kind: OpRespond, Token: "tw", MsgSerial: n, Disposition: "queue", QueueDebt: true}, t0)
		queued = append(queued, n)
	}
	_, _, err := s.Apply(&Op{
		Kind: OpSendMessage, Token: "tl", To: "worker", MsgType: MsgRequest, Body: "overflow", QueueDebt: true,
	}, t0)
	if err == nil {
		t.Fatal("new admission ignored accepted debt capacity")
	}
	for _, n := range queued {
		if m := s.Messages[n]; m == nil || !m.Owed(t0) {
			t.Fatal("capacity refusal evicted existing accepted debt")
		}
	}
	// Historical unmarked sends retain their old replay semantics. Acceptance
	// of that retained pending mail still observes the new queue bound.
	mail := mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "tl", To: "worker", MsgType: MsgRequest, Body: "old pending",
	}, t0)
	n := mail["msg_serial"].(uint64)
	if _, _, err := s.Apply(&Op{
		Kind: OpRespond, Token: "tw", MsgSerial: n, Disposition: "queue", QueueDebt: true,
	}, t0); err == nil {
		t.Fatal("acceptance bypassed the capacity bound")
	}
	mustApply(t, s, &Op{Kind: OpRespond, Token: "tw", MsgSerial: queued[0], Disposition: "decline"}, t0)
	mustApply(t, s, &Op{Kind: OpRespond, Token: "tw", MsgSerial: n, Disposition: "queue", QueueDebt: true}, t0)
	if s.AcceptedDebtCount("worker") != 2 {
		t.Fatal("declining did not release the accepted debt slot")
	}
}
