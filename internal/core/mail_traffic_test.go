// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func trafficFold(t *testing.T, s *State, op *Op, at time.Time) Result {
	t.Helper()
	r, _, err := s.Apply(op, at)
	if err != nil {
		t.Fatalf("%s: %v", op.Kind, err)
	}
	return r
}

func trafficActor(t *testing.T, s *State, name string, at time.Time) {
	t.Helper()
	trafficFold(t, s, &Op{Kind: OpRegister, Name: name, NewToken: name + "-token"}, at)
	trafficFold(t, s, &Op{Kind: OpAckBoard, Token: name + "-token"}, at)
}

func TestMailTrafficV1CountsOnlyNewLedgeredSends(t *testing.T) {
	s := NewState("traffic", DefaultLimits())
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	trafficActor(t, s, "sender", at)
	trafficActor(t, s, "reader", at)
	op := &Op{Kind: OpSendMessage, Token: "sender-token", To: "reader", MsgType: MsgQuestion, Body: "PRIVATE", OpID: "old"}
	trafficFold(t, s, op, at)
	if s.Traffic != nil {
		t.Fatal("old historical fold changed")
	}
	op.MailTrafficV1 = true
	op.OpID = "new"
	trafficFold(t, s, op, at)
	serial := s.Serial
	trafficFold(t, s, op, at)
	if s.Serial != serial || s.Traffic.FirstSerial != serial {
		t.Fatal("dedup changed serial/coverage")
	}
	view := s.MailTraffic(at, "sender", true)
	if len(view.Days) != 1 || view.Days[0].Sent.Question != 1 || view.Days[0].AsksReceived.Question != 1 {
		t.Fatalf("counts: %+v", view)
	}
	view.Days[0].Agents[0].Sent.Question = 99
	if s.MailTraffic(at, "sender", true).Days[0].Agents[0].Sent.Question != 1 {
		t.Fatal("projection aliases state")
	}
}

func TestMailTrafficV1UTCRetentionAndClockRollback(t *testing.T) {
	s := NewState("traffic", DefaultLimits())
	start := time.Date(2026, 9, 1, 23, 30, 0, 0, time.FixedZone("west", -7*3600))
	trafficActor(t, s, "sender", start)
	trafficActor(t, s, "reader", start)
	send := func(at time.Time) {
		trafficFold(t, s, &Op{Kind: OpSendMessage, Token: "sender-token", To: "reader", MsgType: MsgNotify, Body: "count", MailTrafficV1: true}, at)
	}
	for i := range 41 {
		send(start.AddDate(0, 0, i))
	}
	last := start.AddDate(0, 0, 40)
	if len(s.Traffic.Days) != 35 || s.Traffic.Days[last.UTC().Format(time.DateOnly)] == nil {
		t.Fatalf("UTC window: %+v", s.Traffic)
	}
	send(start.AddDate(0, 0, -500))
	if len(s.Traffic.Days) != 35 {
		t.Fatal("rollback expanded retained window")
	}
	before, _ := json.Marshal(s)
	if got := s.MailTraffic(last.AddDate(0, 0, 36), "", false); len(got.Days) != 0 {
		t.Fatalf("quiet board didn't expire: %+v", got)
	}
	after, _ := json.Marshal(s)
	if !bytes.Equal(before, after) {
		t.Fatal("reading quiet expiry mutated replayable state")
	}
}

func TestMailTrafficV1OverflowIsExplicitAndTotalsRemainExact(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxAgents = 1024
	s := NewState("traffic", lim)
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	trafficActor(t, s, "reader", at)
	for i := range 513 {
		name := fmt.Sprintf("sender-%03d", i)
		trafficActor(t, s, name, at)
		kind := MsgNotify
		if i == 512 {
			kind = MsgQuestion
		}
		trafficFold(t, s, &Op{Kind: OpSendMessage, Token: name + "-token", To: "reader", MsgType: kind, Body: "PRIVATE", MailTrafficV1: true, MailboxCapacity: true}, at)
	}
	view := s.MailTraffic(at, "", true)
	d := view.Days[0]
	if len(d.Agents) != 512 || d.RowsComplete || d.Sent.Notify != 512 || d.Sent.Question != 1 || d.AsksReceived.Question != 1 || d.OverflowSent.Question != 1 || d.OverflowAsksReceived.Question != 1 {
		t.Fatalf("overflow: %+v rows%d", d, len(d.Agents))
	}
	if len(s.MailTraffic(at, "sender-512", true).Days[0].Agents) != 0 {
		t.Fatal("overflow pretended to have an exact agent row")
	}
}

func TestMailTrafficV1MaximumCounterStorageReceipt(t *testing.T) {
	h := &TrafficHistory{Days: map[string]*TrafficBucket{}}
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for d := range TrafficRetentionDays {
		b := &TrafficBucket{Agents: map[string]*TrafficAgentCounts{}}
		for i := range TrafficAgentsPerDay {
			b.Agents[fmt.Sprintf("%s%03d", strings.Repeat("a", 125), i)+"\x0018446744073709551615"] = &TrafficAgentCounts{CreatedSerial: ^uint64(0), Sent: TrafficCounts{Notify: ^uint64(0), Question: ^uint64(0), Request: ^uint64(0), Handoff: ^uint64(0)}, AsksReceived: TrafficCounts{Question: ^uint64(0), Request: ^uint64(0), Handoff: ^uint64(0)}}
		}
		h.Days[start.AddDate(0, 0, d).Format(time.DateOnly)] = b
	}
	raw, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot bytes.Buffer
	if err := gob.NewEncoder(&snapshot).Encode(h); err != nil {
		t.Fatal(err)
	}
	t.Logf("35*512 rows, 128-byte IDs and max uint64 counters: gob %d bytes; JSON %d bytes; raw uint64 counter payload %d bytes", snapshot.Len(), len(raw), 35*512*8*8)
}
