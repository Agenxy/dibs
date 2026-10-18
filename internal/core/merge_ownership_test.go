// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"testing"
	"time"
)

func TestSupportedMergePreservesIncarnationBoundariesAndRetainedCustody(t *testing.T) {
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	s := NewState("merge-fixture", DefaultLimits())
	from := seat(t, s, "fork", "fork-nonce", now)
	into := seat(t, s, "survivor", "survivor-nonce", now)
	// The production API tests exercise normal creation. These explicit bounds
	// model a reused source id retaining a predecessor's excluded mail.
	from.CreatedSerial, from.TruncatedBefore = 10, 10
	into.CreatedSerial = 20
	from.Status = StatusDormant
	s.Serial = 30
	s.Messages[5] = &Message{Serial: 5, To: from.ID, From: "peer", Type: MsgNotify}
	s.Messages[11] = &Message{Serial: 11, To: from.ID, From: "peer", Type: MsgQuestion}
	s.Messages[12] = &Message{Serial: 12, From: from.ID, To: "peer", Type: MsgQuestion}
	s.Messages[13] = &Message{Serial: 13, To: from.ID, From: "peer", Type: MsgRequest, State: MsgStateApproved, QueueDebt: true}
	s.Messages[14] = &Message{Serial: 14, To: from.ID, From: "peer", Type: MsgNotify, State: MsgStatePending}
	s.Messages[6] = &Message{Serial: 6, From: from.ID, To: "peer", Type: MsgQuestion}
	plan := s.MergePlan(from.ID, into.ID, 0)
	if plan["outgoing_rewrites"] != 1 || plan["messages"].(map[string]map[string]int)[MsgQuestion][""] != 1 {
		t.Fatal("setup/preview counted excluded predecessor mail")
	}
	op := &Op{
		Kind: OpMergeAgents, To: from.ID, MergeInto: into.ID,
		MergeActor: &MergeActor{ID: "person", Authority: "human", Evidence: MergeEvidence{Known: true}},
	}
	if err := s.Admit(op); err != nil {
		t.Fatal(err)
	}
	_, events, err := s.Apply(op, now)
	if err != nil {
		t.Fatal(err)
	}
	arrivals := map[uint64]bool{}
	for _, ev := range events {
		if ev.Type == "message.adopted" && ev.To == into.ID {
			arrivals[ev.Data["msg_serial"].(uint64)] = true
		}
	}
	if !arrivals[13] || !arrivals[14] {
		t.Fatal("accepted debt or unread FYI moved without an arrival event")
	}
	if plan["owed_requests"] != 1 {
		t.Fatal("preview omitted accepted debt subset")
	}

	for _, serial := range []uint64{11, 12} {
		if allowed, _ := MessageAccess(serial, s.Messages[serial], into); !allowed {
			t.Fatalf("owned mail %d unreadable", serial)
		}
	}
	for _, serial := range []uint64{5, 6} {
		if allowed, _ := MessageAccess(serial, s.Messages[serial], into); allowed {
			t.Fatalf("predecessor mail %d disclosed", serial)
		}
	}
	newer := &Agent{ID: into.ID, CreatedSerial: s.Serial + 1}
	if allowed, _ := MessageAccess(12, s.Messages[12], newer); allowed {
		t.Fatal("later incarnation inherited sender grant")
	}
	clear(s.Messages) // retention must not undo nonce-only recovery protection
	if !s.HoldsAdoptedMail(into.ID) {
		t.Fatal("retention removed merged-mail custody")
	}
}

func TestSupportedMergeTransfersExclusiveMembershipWithoutAQueuedGhost(t *testing.T) {
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	s := NewState("merge-fixture", DefaultLimits())
	from := seat(t, s, "fork", "fork-nonce", now)
	into := seat(t, s, "survivor", "survivor-nonce", now)
	from.Status = StatusDormant
	ch := &Space{
		ID: "work", Owner: from.ID,
		Members: map[string]*Membership{from.ID: {Agent: from.ID, JoinedSerial: 1}},
		Subs:    map[string]bool{from.ID: true}, Declined: map[string]bool{},
		Queue:   []string{"first", into.ID, "last"},
		Pending: map[string]*Membership{into.ID: {Agent: into.ID}},
	}
	s.Spaces[ch.ID] = ch
	before := s.MergeFence(from.ID, into.ID, 0)
	ch.Queue = append(ch.Queue, "later")
	if before == s.MergeFence(from.ID, into.ID, 0) {
		t.Fatal("queue mutation did not invalidate preview")
	}
	op := &Op{
		Kind: OpMergeAgents, To: from.ID, MergeInto: into.ID,
		MergeActor: &MergeActor{ID: "person", Authority: "human", Evidence: MergeEvidence{Known: true}},
	}
	if _, _, err := s.Apply(op, now); err != nil {
		t.Fatal(err)
	}
	if ch.Owner != into.ID || ch.Members[into.ID] == nil || ch.Members[into.ID].Agent != into.ID {
		t.Fatal("exclusive membership did not transfer")
	}
	if len(ch.Queue) != 3 || ch.Queue[0] != "first" || ch.Queue[1] != "last" || ch.Pending[into.ID] != nil {
		t.Fatal("survivor became its own queued ghost or unrelated queue order changed")
	}
	if ch.Members[from.ID] != nil || ch.Subs[from.ID] || !ch.Subs[into.ID] {
		t.Fatal("retired participant still owns membership")
	}
}

func TestSupportedMergeClaimPathsCarryTheirMachine(t *testing.T) {
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	s := NewState("merge-fixture", DefaultLimits())
	from := seat(t, s, "fork", "fork-nonce", now)
	into := seat(t, s, "survivor", "survivor-nonce", now)
	from.Status = StatusDormant
	s.Claims = []*Claim{{Agent: from.ID, Path: "/work", Host: "a"}, {Agent: into.ID, Path: "/work", Host: "b"}}
	if err := s.MergeSafety(from.ID, into.ID); err != nil {
		t.Fatal("different machines' paths conflated", err)
	}
	s.Claims[1].Host = "a"
	if err := s.MergeSafety(from.ID, into.ID); err == nil {
		t.Fatal("same-machine conflicting claims accepted")
	}
	s.Claims[1].Host = ""
	if err := s.MergeSafety(from.ID, into.ID); err == nil {
		t.Fatal("unknown host treated as proof of difference")
	}
}
