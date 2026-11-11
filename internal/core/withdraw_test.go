// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"encoding/json"
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
	if !sameState(replayed, f.s) {
		t.Fatal("live state differs from replay")
	}
	withdrawRefused(t, f, Op{Kind: OpWithdrawMessage, Token: "t-lead", MsgSerial: a}, "E_MSG_FINAL")
	withdrawRefused(t, f, Op{Kind: OpWithdrawMessage, Token: "t-worker", MsgSerial: b}, "E_NOT_SENDER")
	withdrawRefused(t, f, Op{Kind: OpWithdrawMessage, Token: "t-lead", MsgSerial: b, SupersededBy: 999}, "E_NO_MESSAGE")
}

func TestWithdrawalRefusesInheritedSenderAndPerformedEffect(t *testing.T) {
	f := newQueueFoldFixture(t)
	n := f.queue("", t0)
	// A historical board could leave outgoing mail behind a reused ID. This
	// fixture is precisely that replay state, not a modern purge's retired From.
	f.s.Agents["lead"].CreatedSerial = n + 1
	withdrawRefused(t, f, Op{Kind: OpWithdrawMessage, Token: "t-lead", MsgSerial: n}, "E_NOT_SENDER")
	f.s.Agents["lead"].CreatedSerial = 1
	m := f.s.Messages[n]
	m.State, m.Grant = MsgStateApproved, RoleCoordinator
	withdrawRefused(t, f, Op{Kind: OpWithdrawMessage, Token: "t-lead", MsgSerial: n}, "E_MSG_FINAL")
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

func TestQuestionWithdrawalOwnershipFinalityAndExactFold(t *testing.T) {
	f := newQueueFoldFixture(t)
	send := func(kind string) uint64 {
		return f.apply(Op{Kind: OpSendMessage, Token: "t-lead", To: "worker", MsgType: kind, Body: "question"})["msg_serial"].(uint64)
	}
	n, replacement, wrongType := send(MsgQuestion), send(MsgRequest), send(MsgHandoff)
	withdrawRefused(t, f, Op{Kind: OpWithdrawMessage, Token: "t-worker", MsgSerial: n}, "E_NOT_SENDER")
	withdrawRefused(t, f, Op{Kind: OpWithdrawMessage, Token: "t-lead", MsgSerial: wrongType}, "E_BAD_DISPOSITION")
	withdrawRefused(t, f, Op{Kind: OpWithdrawMessage, Token: "t-lead", MsgSerial: n, SupersededBy: wrongType}, "E_NO_MESSAGE")
	f.s.Agents["lead"].CreatedSerial = n + 1
	withdrawRefused(t, f, Op{Kind: OpWithdrawMessage, Token: "t-lead", MsgSerial: n}, "E_NOT_SENDER")
	f.s.Agents["lead"].CreatedSerial = 0
	f.apply(Op{Kind: OpWithdrawMessage, Token: "t-lead", MsgSerial: n, Body: "answered elsewhere", SupersededBy: replacement})
	// Restore the fixture's preexisting creation fence before replay comparison.
	// These temporary checks did not append an operation.
	f.s.Agents["lead"].CreatedSerial = 1
	withdrawRefused(t, f, Op{Kind: OpWithdrawMessage, Token: "t-lead", MsgSerial: n}, "E_MSG_FINAL")
	f.apply(Op{Kind: OpRespond, Token: "t-worker", MsgSerial: replacement, Disposition: "deny", Body: "no"})
	withdrawRefused(t, f, Op{Kind: OpWithdrawMessage, Token: "t-lead", MsgSerial: replacement}, "E_MSG_FINAL")
	if m := f.s.Messages[n]; m.State != MsgStateWithdrawn || m.Response != "" || m.WithdrawalReason != "answered elsewhere" {
		t.Fatalf("withdrawal forged an answer: %+v", m)
	}
	f.replay()
}

func withdrawRefused(t *testing.T, f *queueFoldFixture, op Op, code string) {
	t.Helper()
	before, err := json.Marshal(f.s)
	if err != nil {
		t.Fatal(err)
	}
	err = f.s.Admit(&op)
	if e, ok := err.(*Error); !ok || e.Code != code {
		t.Fatalf("Admit withdrawal = %v, want %s", err, code)
	}
	after, err := json.Marshal(f.s)
	if err != nil || string(before) != string(after) {
		t.Fatal("refused admission changed replayable state")
	}
}

func TestNotifyWithdrawalValidationDoesNotChangeTheFold(t *testing.T) {
	f := newQueueFoldFixture(t)
	msg := f.apply(Op{Kind: OpSendMessage, Token: "t-lead", To: "worker", MsgType: MsgNotify, Body: "presented"})["msg_serial"].(uint64)
	f.apply(Op{Kind: OpAckMessage, Token: "t-worker", MsgSerial: msg})
	op := Op{Kind: OpWithdrawMessage, Token: "t-lead", MsgSerial: msg}
	withdrawRefused(t, f, op, "E_MSG_FINAL")
	// A future stricter validator must never reject a formerly accepted ledger
	// entry during replay. Admission alone owns presentation eligibility.
	result, _, err := f.s.Apply(&op, t0)
	if err != nil || result["state"] != MsgStateWithdrawn {
		t.Fatalf("fold revalidated withdrawal: %v / %v", result, err)
	}
}
