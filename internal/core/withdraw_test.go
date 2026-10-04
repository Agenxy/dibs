package core

import (
	"reflect"
	"testing"
)

func TestWithdrawalClearsLockedQueueAndReplaysExactly(t *testing.T) {
	f := newQueueFoldFixture(t)
	a, b := f.queue("high", t0), f.queue("low", t0)
	f.apply(Op{Kind: OpGrantPermission, To: "worker", Mode: PermQueueOrderLock, MsgSerial: a})
	f.order(a, b)
	f.apply(Op{Kind: OpWithdrawMessage, Token: "t-lead", MsgSerial: a, Body: "reassigned", SupersededBy: b})
	f.order(b)
	m := f.s.Messages[a]
	if m.Owed(t0) || m.QueueDebt || m.QueueOrderLocked || m.QueueRank != 0 || m.Consumed || !m.Terminal() {
		t.Fatalf("withdrawn obligation/receipt: %+v", m)
	}
	if f.s.Messages[b].QueueRank != 1 {
		t.Fatal("remaining queue was not compacted")
	}
	replayed := NewState("queue", DefaultLimits())
	for _, op := range f.ops {
		if _, _, err := replayed.Apply(&op, t0); err != nil {
			t.Fatal("replay:", err)
		}
	}
	if !reflect.DeepEqual(replayed, f.s) {
		t.Fatal("live state differs from replay")
	}
	f.refused(Op{Kind: OpWithdrawMessage, Token: "t-lead", MsgSerial: a}, "E_MSG_FINAL")
	f.refused(Op{Kind: OpWithdrawMessage, Token: "t-worker", MsgSerial: b}, "E_NOT_SENDER")
	f.refused(Op{Kind: OpWithdrawMessage, Token: "t-lead", MsgSerial: b, SupersededBy: 999}, "E_NO_MESSAGE")
}

func TestWithdrawalRefusesInheritedSenderAndPerformedEffect(t *testing.T) {
	f := newQueueFoldFixture(t)
	n := f.queue("", t0)
	// A historical board could leave outgoing mail behind a reused ID. This
	// fixture is precisely that replay state, not a modern purge's retired From.
	f.s.Agents["lead"].CreatedSerial = n + 1
	f.refused(Op{Kind: OpWithdrawMessage, Token: "t-lead", MsgSerial: n}, "E_NOT_SENDER")
	f.s.Agents["lead"].CreatedSerial = 1
	m := f.s.Messages[n]
	m.State, m.Grant = MsgStateApproved, RoleCoordinator
	f.refused(Op{Kind: OpWithdrawMessage, Token: "t-lead", MsgSerial: n}, "E_MSG_FINAL")
}

func TestWithdrawalShapeStaysAtAdmission(t *testing.T) {
	for _, op := range []Op{
		{Kind: OpWithdrawMessage},
		{Kind: OpWithdrawMessage, MsgSerial: 1, SupersededBy: 1},
		{Kind: OpWithdrawMessage, MsgSerial: 1, Milestone: 1},
		{Kind: OpWithdrawMessage, MsgSerial: 1, Deliverable: "not delivered"},
		{Kind: OpRespond, MsgSerial: 1, Disposition: "approve", SupersededBy: 2},
	} {
		if err := Admit(&op, DefaultLimits()); err == nil {
			t.Fatalf("admitted malformed withdrawal: %+v", op)
		}
	}
}
