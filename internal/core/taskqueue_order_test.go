// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

type queueFoldFixture struct {
	s    *State
	ops  []Op
	test *testing.T
}

func newQueueFoldFixture(t *testing.T) *queueFoldFixture {
	t.Helper()
	f := &queueFoldFixture{s: NewState("queue", DefaultLimits()), test: t}
	for _, name := range []string{"lead", "worker"} {
		f.apply(Op{Kind: OpRegister, Name: name, NewToken: "t-" + name, Nonce: "queue-invariant-nonce-" + name, AgentKind: KindPersistent})
		f.apply(Op{Kind: OpAckBoard, Token: "t-" + name})
	}
	return f
}

func (f *queueFoldFixture) apply(op Op) Result {
	f.test.Helper()
	if err := Admit(&op, f.s.Limits); err != nil {
		f.test.Fatal("admit:", err)
	}
	res, evs, err := f.s.Apply(&op, t0)
	if err != nil {
		f.test.Fatal(op.Kind, err)
	}
	if len(evs) > 0 {
		f.ops = append(f.ops, op)
	}
	return res
}

func (f *queueFoldFixture) queue(priority string, deadline time.Time) uint64 {
	f.test.Helper()
	res := f.apply(Op{
		Kind: OpSendMessage, Token: "t-lead", To: "worker", MsgType: MsgRequest,
		Body: "work", RequestPriority: priority, DeadlineSec: int(max(0, deadline.Sub(t0).Seconds())), QueueDebt: true,
	})
	n := res["msg_serial"].(uint64)
	f.apply(Op{Kind: OpRespond, Token: "t-worker", MsgSerial: n, Disposition: "queue", QueueDebt: true})
	return n
}

func (f *queueFoldFixture) order(want ...uint64) {
	f.test.Helper()
	var got []uint64
	for _, m := range f.s.TaskQueue("worker") {
		got = append(got, m.Serial)
	}
	if !reflect.DeepEqual(got, want) {
		f.test.Fatalf("order %v, want %v", got, want)
	}
}

func (f *queueFoldFixture) refused(op Op, code string) {
	f.test.Helper()
	before, err := json.Marshal(f.s)
	if err != nil {
		f.test.Fatal(err)
	}
	_, evs, err := f.s.Apply(&op, t0)
	var ce *Error
	if !errors.As(err, &ce) || ce.Code != code || len(evs) != 0 {
		f.test.Fatalf("refusal %v events %v, want %s", err, evs, code)
	}
	after, err := json.Marshal(f.s)
	if err != nil || string(before) != string(after) {
		f.test.Fatal("refused operation changed replayable state")
	}
}

func (f *queueFoldFixture) replay() {
	f.test.Helper()
	r := NewState("queue", DefaultLimits())
	for i := range f.ops {
		if _, _, err := r.Apply(&f.ops[i], t0); err != nil {
			f.test.Fatal("replay:", err)
		}
	}
	got, err := json.Marshal(r)
	if err != nil {
		f.test.Fatal(err)
	}
	want, err := json.Marshal(f.s)
	if err != nil || string(got) != string(want) {
		f.test.Fatal("accepted ordering operations do not replay to identical state")
	}
}

func TestQueueManualOrderSurvivesArrivalAndPriorityReset(t *testing.T) {
	f := newQueueFoldFixture(t)
	a := f.queue("low", time.Time{})
	b := f.queue("high", t0.Add(time.Hour))
	c := f.queue("urgent", t0.Add(2*time.Hour))
	d := f.queue("high", t0.Add(30*time.Minute))
	f.order(c, d, b, a)
	f.apply(Op{Kind: OpQueueUpdate, Token: "t-worker", MsgSerial: a, QueueBefore: b})
	e := f.queue("normal", time.Time{})
	f.order(c, d, e, a, b) // arrival preserves all manually ordered sibling relations
	f.apply(Op{Kind: OpQueueUpdate, Token: "t-worker", MsgSerial: b, QueuePriority: "urgent"})
	f.order(b, c, d, e, a)
	if f.s.Messages[b].RequestPriority != "high" {
		t.Fatal("recipient override rewrote the sender's request")
	}
	f.apply(Op{Kind: OpQueueUpdate, Token: "t-worker", MsgSerial: b, QueueResetPriority: true})
	f.order(c, d, b, e, a)
	f.apply(Op{Kind: OpQueueUpdate, Token: "t-worker", MsgSerial: b, QueueTail: true})
	f.order(c, d, e, a, b)
	serial := f.s.Serial
	res := f.apply(Op{Kind: OpQueueUpdate, Token: "t-worker", MsgSerial: b, QueueTail: true})
	if res["changed"] != false || f.s.Serial != serial {
		t.Fatal("idempotent order retry advanced the ledger serial")
	}
	f.refused(Op{Kind: OpQueueUpdate, Token: "t-worker", MsgSerial: b, QueueBefore: 999999}, "E_BAD_ARG")
	f.refused(Op{Kind: OpQueueUpdate, Token: "t-lead", MsgSerial: b, QueueTail: true}, "E_NO_MESSAGE")
	f.apply(Op{Kind: OpRespond, Token: "t-worker", MsgSerial: a, Disposition: "approve", QueueDebt: true})
	f.refused(Op{Kind: OpQueueUpdate, Token: "t-worker", MsgSerial: a, QueueTail: true}, "E_BAD_DISPOSITION")
	f.replay()
}

func TestQueueLocksProtectCrossingAndIncarnationButNeverStarting(t *testing.T) {
	f := newQueueFoldFixture(t)
	a, b, c := f.queue("normal", time.Time{}), f.queue("normal", time.Time{}), f.queue("normal", time.Time{})
	f.apply(Op{Kind: OpGrantRole, To: "lead", Mode: RoleCoordinator})
	lock := Op{
		Kind: OpGrantPermission, To: "worker", Mode: PermQueueOrderLock, MsgSerial: b,
		PermissionActor: "lead", PermissionActorCreated: f.s.Agents["lead"].CreatedSerial,
	}
	f.apply(lock)
	serial := f.s.Serial
	if res := f.apply(lock); res["changed"] != false || f.s.Serial != serial {
		t.Fatal("repeated scoped grant advanced the serial")
	}
	f.refused(Op{Kind: OpQueueUpdate, Token: "t-worker", MsgSerial: a, QueueTail: true}, "E_QUEUE_LOCKED")
	f.refused(Op{Kind: OpQueueUpdate, Token: "t-worker", MsgSerial: b, QueuePriority: "urgent"}, "E_QUEUE_LOCKED")
	f.order(a, b, c)
	f.apply(Op{Kind: OpRespond, Token: "t-worker", MsgSerial: b, Disposition: "approve", QueueDebt: true})
	f.order(a, c) // a locked task may start without unlocking
	lock.MsgSerial = c
	lock.PermissionActorCreated++
	f.refused(lock, "E_NOT_PERMITTED")
	lock.PermissionActorCreated = f.s.Agents["lead"].CreatedSerial
	lock.PermissionActor = "worker"
	lock.PermissionActorCreated = f.s.Agents["worker"].CreatedSerial
	f.refused(lock, "E_NOT_PERMITTED")
	lock.PermissionActor = HumanActor
	lock.MsgSerial = 0
	f.apply(lock)
	f.refused(Op{Kind: OpQueueUpdate, Token: "t-worker", MsgSerial: a, QueueTail: true}, "E_QUEUE_LOCKED")
	lock.Kind = OpRevokePermission
	f.apply(lock)
	lock.Kind = OpGrantPermission
	lock.MsgSerial = c
	f.apply(lock)
	lock.Kind = OpRevokePermission
	f.apply(lock)
	f.apply(Op{Kind: OpQueueUpdate, Token: "t-worker", MsgSerial: a, QueueTail: true})
	f.order(c, a)
	f.replay()
}
