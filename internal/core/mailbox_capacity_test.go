// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"errors"
	"fmt"
	"testing"
)

func TestBoardCapacityCountsStayWithTheirRecipient(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxMailboxDepth = 5
	s := NewState("test", lim)
	for _, id := range []string{"sender", "reader", "other"} {
		reg(t, s, id, "token-"+id, t0)
	}
	for range 4 {
		id := mustApply(t, s, &Op{
			Kind: OpSendMessage, Token: "token-sender", To: "reader", MsgType: MsgRequest, Body: "work",
		}, t0)["msg_serial"].(uint64)
		mustApply(t, s, &Op{Kind: OpRespond, Token: "token-reader", MsgSerial: id, Disposition: "queue", QueueDebt: true}, t0)
		id = mustApply(t, s, &Op{
			Kind: OpSendMessage, Token: "token-sender", To: "other", MsgType: MsgNotify, Body: "FYI",
		}, t0)["msg_serial"].(uint64)
		mustApply(t, s, &Op{Kind: OpActivityCheckpoint, Token: "token-other", NotifyAnnounced: []uint64{id}}, t0)
	}
	rows := s.Board()["agents"].([]map[string]any)
	for _, row := range rows {
		if row["id"] == "reader" {
			c, ok := row["mailbox_capacity"].(MailboxCapacity)
			if !ok || c.Depth != 4 || c.AcceptedDebt != 4 || c.AnnouncedFYIs != 0 {
				t.Fatalf("reader projection lost queued debt: %v", row)
			}
		} else if row["mailbox_capacity"] != nil {
			t.Fatalf("another recipient inherited reader capacity: %v", row)
		}
	}
}

// Keep the measured fleet workload available without imposing machine-specific
// timing thresholds on correctness tests. Setup is metadata only, outside timing.
func BenchmarkMailboxCapacityBoard(b *testing.B) {
	for _, n := range []int{100, 200, 400} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			s := NewState("bench", DefaultLimits())
			var serial uint64
			for i := range n {
				name := fmt.Sprintf("worker-%d", i)
				s.Agents[name] = &Agent{ID: name, Name: name, Status: StatusActive}
				for range 128 {
					serial++
					s.Messages[serial] = &Message{Serial: serial, To: name, Type: MsgRequest, State: MsgStatePending}
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				s.Board()
			}
		})
	}
}

func TestUnmarkedHistoricalSendsKeepOriginalCapacityFold(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxMailboxDepth = 2
	s := NewState("test", lim)
	reg(t, s, "sender", "ts", t0)
	reg(t, s, "reader", "tr", t0)
	var first uint64
	for range 2 {
		id := mustApply(t, s, &Op{Kind: OpSendMessage, Token: "ts", To: "reader", MsgType: MsgNotify, Body: "historical"}, t0)["msg_serial"].(uint64)
		if first == 0 {
			first = id
		}
		mustApply(t, s, &Op{Kind: OpActivityCheckpoint, Token: "tr", NotifyAnnounced: []uint64{id}}, t0)
	}
	if _, _, err := s.Apply(&Op{Kind: OpSendMessage, Token: "ts", To: "reader", MsgType: MsgRequest, Body: "old ask"}, t0); !errors.Is(err, ErrMailboxFull) {
		t.Fatalf("rewrote historical admission: %v", err)
	}
	mustApply(t, s, &Op{Kind: OpSendMessage, Token: "ts", To: "reader", MsgType: MsgNotify, Body: "old notify"}, t0)
	if s.Messages[first].State != MsgStateDisplaced {
		t.Fatal("historical notify no longer displaces")
	}
}

func TestMailboxBulkAdmissionRejectsFutureAndMixedPrefix(t *testing.T) {
	s := NewState("test", DefaultLimits())
	reg(t, s, "reader", "tr", t0)
	for _, op := range []*Op{
		{Kind: OpAckMessage, Token: "tr", NotifyThroughSerial: s.Serial + 1},
		{Kind: OpAckMessage, Token: "tr", NotifyThroughSerial: s.Serial, MsgSerial: 1},
		{Kind: OpSendMessage, Token: "tr", NotifyThroughSerial: s.Serial},
	} {
		before := s.Serial
		if err := s.Admit(op); err == nil {
			t.Fatalf("bad prefix admitted: %+v", op)
		}
		if s.Serial != before {
			t.Fatal("admission changed state")
		}
	}
}

func TestRetainedExemptFYIsHaveSeparateBound(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxMailboxDepth = 2
	s := NewState("test", lim)
	reg(t, s, "sender", "ts", t0)
	reg(t, s, "reader", "tr", t0)
	for range 8 {
		op := &Op{Kind: OpSendMessage, Token: "ts", To: "reader", MsgType: MsgNotify, Body: "fyi", MailboxCapacity: true, QueueDebt: true}
		if err := s.Admit(op); err != nil {
			t.Fatal(err)
		}
		id := mustApply(t, s, op, t0)["msg_serial"].(uint64)
		mustApply(t, s, &Op{Kind: OpActivityCheckpoint, Token: "tr", NotifyAnnounced: []uint64{id}}, t0)
	}
	c := s.MailboxCapacity("reader")
	if c.Depth != 0 || c.AnnouncedFYIs != 2 {
		t.Fatalf("unbounded retained FYIs: %+v", c)
	}
}
