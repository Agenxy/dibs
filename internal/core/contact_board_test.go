// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"encoding/json"
	"testing"
	"time"
)

func TestPostedContactRemainsOnBoardUntilResolved(t *testing.T) {
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	s := NewState("contact-board", DefaultLimits())
	ackReg(t, s, "sender", "sender", now)
	ackReg(t, s, "recipient", "recipient", now)
	serial := mustApply(t, s, &Op{Kind: OpSendMessage, Token: "sender", To: "recipient", MsgType: MsgRequest, Body: "private mail"}, now)["msg_serial"].(uint64)
	op := &Op{Kind: OpContactEscalate, MsgSerial: serial}
	if err := s.Admit(op); err != nil {
		t.Fatal("setup contact:", err)
	}
	contact := mustApply(t, s, op, now)["contact_serial"].(uint64)
	op = &Op{Kind: OpContactNotified, ContactSerial: contact}
	if err := s.Admit(op); err != nil {
		t.Fatal("setup posted receipt:", err)
	}
	mustApply(t, s, op, now.Add(time.Second))
	before, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	alerts, ok := s.Board()["contact_alerts"].([]*ContactEscalation)
	if !ok || len(alerts) != 1 || alerts[0].Serial != contact || !alerts[0].NotifiedAt.Equal(now.Add(time.Second)) || !alerts[0].ResolvedAt.IsZero() {
		t.Fatalf("posted but unread contact disappeared from the board: %+v", alerts)
	}
	after, err := json.Marshal(s)
	if err != nil || string(before) != string(after) {
		t.Fatal("reading the board changed folded state:", err)
	}
	op = &Op{Kind: OpContactResolved, ContactSerial: contact}
	if err := s.Admit(op); err != nil {
		t.Fatal("setup resolution:", err)
	}
	mustApply(t, s, op, now.Add(2*time.Second))
	if alerts := s.Board()["contact_alerts"]; alerts != nil {
		t.Fatalf("resolved contact remains on the board: %+v", alerts)
	}
}
