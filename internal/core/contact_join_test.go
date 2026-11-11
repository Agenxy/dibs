// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"testing"
	"time"
)

func TestDiagnosedContactJoinsAfterPostedWindow(t *testing.T) {
	f := newQueueFoldFixture(t)
	send := func() uint64 {
		return f.apply(Op{Kind: OpSendMessage, Token: "t-lead", To: "worker", MsgType: MsgRequest, Body: "pending"})["msg_serial"].(uint64)
	}
	first := send()
	contact := f.apply(Op{Kind: OpContactDiagnosed, MsgSerial: first, ContactReason: ContactTurnsEnded})["contact_serial"].(uint64)
	f.apply(Op{Kind: OpContactNotified, ContactSerial: contact})
	second := send()
	// Use the literal on the old-code control so an unknown operation is a
	// runtime refusal, rather than a compilation failure.
	op := &Op{Kind: "contact_joined", ContactSerial: contact, MsgSerial: second, AgentID: "lead"}
	if err := f.s.Admit(op); err != nil {
		t.Fatal(err)
	}
	before := f.s.Serial
	result, events, err := f.s.Apply(op, t0.Add(20*time.Minute))
	if err != nil {
		t.Fatal("episode join:", err)
	}
	if f.s.Serial != before+1 || len(events) != 0 || result["contact_serial"] != contact || len(f.s.Contacts) != 1 ||
		f.s.Contacts[contact].Count != 2 || f.s.Messages[second].ContactEscalatedAt != contact {
		t.Fatalf("join changed page or lost source: %v %v", result, events)
	}
	f.ops = append(f.ops, *op)
	f.replay()
}
