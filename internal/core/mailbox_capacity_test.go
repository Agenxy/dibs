// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"errors"
	"testing"
)

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
