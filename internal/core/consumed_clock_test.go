// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"encoding/json"
	"testing"
	"time"
)

// Ordinary consumed mail has no Owed exception. A wall-only gap past its
// fifteen-minute legacy window must prune it on the live mutation door just
// as it does after the ledger strips process-local monotonic readings.
func TestOrdinaryConsumedMailUsesRecordedWallClock(t *testing.T) {
	for _, typ := range []string{MsgQuestion, MsgNotify} {
		t.Run(typ, func(t *testing.T) {
			start := time.Now()
			live := NewState("consumed-clock", DefaultLimits())
			fold := NewState("consumed-clock", DefaultLimits())
			apply := func(op *Op, now time.Time) Result {
				t.Helper()
				res, _, err := live.Apply(op, now)
				if err != nil {
					t.Fatal(err)
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
					t.Fatal(err)
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
					t.Fatalf("live state != fold after %s", op.Kind)
				}
				return res
			}
			for _, name := range []string{"sender", "recipient"} {
				apply(&Op{Kind: OpRegister, Name: name, NewToken: name}, start)
				apply(&Op{Kind: OpAckBoard, Token: name}, start)
			}
			q := apply(&Op{Kind: OpSendMessage, Token: "sender", To: "recipient", MsgType: typ, Body: "ordinary mail", DeadlineSec: 3600}, start)["msg_serial"].(uint64)
			if typ == MsgQuestion {
				apply(&Op{Kind: OpRespond, Token: "recipient", MsgSerial: q, Disposition: "answer", Body: "answer"}, start)
			} else {
				apply(&Op{Kind: OpAckMessage, Token: "recipient", MsgSerial: q}, start)
			}
			if m := live.Messages[q]; m == nil || !m.Terminal() || !m.Consumed || m.Owed(start) {
				t.Fatal("setup: ordinary consumed mail must be terminal and consumed, without an obligation")
			}
			now := shiftedWall(t, start.Add(time.Second), 20*time.Minute)
			apply(&Op{Kind: OpSweep, PurgeMail: true, V7Semantics: true, KeepOwed: true}, now)
			if live.Messages[q] != nil || fold.Messages[q] != nil {
				t.Fatal("consumed mail survived its recorded wall-clock retention window")
			}
		})
	}
}
