// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestContactEscalationCoalescesAndReplays(t *testing.T) {
	base := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	first := NewState("contact", DefaultLimits())
	ackReg(t, first, "sender", "sender", base)
	ackReg(t, first, "recipient", "recipient", base)
	var a, b uint64
	for i := range 10 {
		kind := MsgRequest
		if i%2 == 1 {
			kind = MsgQuestion
		}
		serial := mustApply(t, first, &Op{
			Kind: OpSendMessage, Token: "sender", To: "recipient",
			MsgType: kind, Body: "private content",
		}, base.Add(time.Duration(i)*time.Second))["msg_serial"].(uint64)
		if i == 0 {
			a = serial
		}
		b = serial
		if err := first.Admit(&Op{Kind: OpContactEscalate, MsgSerial: serial}); err != nil {
			t.Fatalf("admit contact %d: %v", serial, err)
		}
		mustApply(t, first, &Op{Kind: OpContactEscalate, MsgSerial: serial}, base.Add(time.Duration(i)*time.Second))
	}
	if len(first.Contacts) != 1 {
		t.Fatalf("ten unread sends in one window produced %d contacts", len(first.Contacts))
	}
	var c *ContactEscalation
	for _, item := range first.Contacts {
		c = item
	}
	if c.Count != 10 || c.OldestSerial != a || c.HighWater != b || c.Recipient != "recipient" || !c.NotifiedAt.IsZero() {
		t.Fatalf("wrong bounded contact summary: %+v", c)
	}
	if err := first.Admit(&Op{Kind: OpContactNotified, ContactSerial: c.Serial}); err != nil {
		t.Fatal(err)
	}
	mustApply(t, first, &Op{Kind: OpContactNotified, ContactSerial: c.Serial}, base.Add(3*time.Second))
	if !c.NotifiedAt.Equal(base.Add(3 * time.Second)) {
		t.Fatalf("posted receipt did not become replayable: %+v", c)
	}
	res := mustApply(t, first, &Op{Kind: OpContactEscalate, MsgSerial: b}, base.Add(4*time.Second))
	if res["deduplicated"] != true || c.Count != 10 {
		t.Fatalf("retry changed contact: %+v %+v", res, c)
	}
	late := mustApply(t, first, &Op{Kind: OpSendMessage, Token: "sender", To: "recipient", MsgType: MsgHandoff, Body: "private C"}, base.Add(11*time.Minute))["msg_serial"].(uint64)
	mustApply(t, first, &Op{Kind: OpContactEscalate, MsgSerial: late}, base.Add(11*time.Minute))
	if len(first.Contacts) != 2 {
		t.Fatalf("new contact window after a posted receipt did not open: %+v", first.Contacts)
	}
	for _, item := range first.Contacts {
		if item.Recipient == "private A" || item.Recipient == "private B" {
			t.Fatal("contact copied private message content")
		}
	}
}

func TestContactAdmissionRejectsFYIAndAlreadyReadMail(t *testing.T) {
	base := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	s := NewState("contact-admit", DefaultLimits())
	ackReg(t, s, "sender", "sender", base)
	ackReg(t, s, "recipient", "recipient", base)
	fyi := mustApply(t, s, &Op{Kind: OpSendMessage, Token: "sender", To: "recipient", MsgType: MsgNotify, Body: "FYI"}, base)["msg_serial"].(uint64)
	if err := s.Admit(&Op{Kind: OpContactEscalate, MsgSerial: fyi}); err == nil {
		t.Fatal("FYI became a human escalation")
	}
	q := mustApply(t, s, &Op{Kind: OpSendMessage, Token: "sender", To: "recipient", MsgType: MsgQuestion, Body: "question"}, base)["msg_serial"].(uint64)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "recipient"}, base.Add(time.Second))
	if err := s.Admit(&Op{Kind: OpContactEscalate, MsgSerial: q}); err == nil {
		t.Fatal("read question became a no-contact escalation")
	}
}

func TestContactFoldReplaysRecordedOperations(t *testing.T) {
	base := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	newBoard := func() *State {
		s := NewState("contact-replay", DefaultLimits())
		ackReg(t, s, "sender", "sender", base)
		ackReg(t, s, "recipient", "recipient", base)
		return s
	}
	a, b := newBoard(), newBoard()
	for _, s := range []*State{a, b} {
		serial := mustApply(t, s, &Op{Kind: OpSendMessage, Token: "sender", To: "recipient", MsgType: MsgRequest, Body: "private"}, base)["msg_serial"].(uint64)
		op := &Op{Kind: OpContactEscalate, MsgSerial: serial}
		if s == a {
			if err := s.Admit(op); err != nil {
				t.Fatal(err)
			}
		}
		mustApply(t, s, op, base.Add(time.Second)) // b models replay: no Admit
	}
	ja, _ := json.Marshal(a.Contacts)
	jb, _ := json.Marshal(b.Contacts)
	if !reflect.DeepEqual(ja, jb) {
		t.Fatalf("contact fold drifted on replay: live=%s replay=%s", ja, jb)
	}
}
