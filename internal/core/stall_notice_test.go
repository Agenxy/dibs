// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"testing"
	"time"
)

func TestStallNoticeAdmitAndAtomicSend(t *testing.T) {
	s := NewState("stall", DefaultLimits())
	ackReg(t, s, "lead", "lead", t0)
	ackReg(t, s, "worker", "worker", t0)
	ackReg(t, s, "reporter", "reporter", t0)
	reporter := s.AgentByToken("reporter")
	reporter.Nonce = DibsNonce
	request := mustApply(t, s, &Op{Kind: OpSendMessage, Token: "lead", To: "worker", MsgType: MsgRequest, Body: "work"}, t0)["msg_serial"].(uint64)
	mustApply(t, s, &Op{Kind: OpRespond, Token: "worker", MsgSerial: request, Disposition: "approve"}, t0)
	version := s.Serial
	op := Op{Kind: OpStallNotified, Token: "reporter", To: "lead", MsgSerial: request, DeclarationSerial: version, Body: "private notice"}
	for _, mutate := range []func(*Op){
		func(o *Op) { o.Token = "lead" },
		func(o *Op) { o.To = "worker" },
		func(o *Op) { o.MsgSerial = 999 },
		func(o *Op) { o.DeclarationSerial = 0 },
		func(o *Op) { o.DeclarationSerial = s.Serial + 1 },
		func(o *Op) { o.Body = "" },
	} {
		bad := op
		mutate(&bad)
		if err := s.Admit(&bad); err == nil {
			t.Errorf("malformed stall notice admitted: %+v", bad)
		}
	}
	if err := s.Admit(&op); err != nil {
		t.Fatal(err)
	}
	// A full mailbox of questions cannot be displaced. Neither serial nor
	// watermark may move when the actual notice send fails.
	s.Limits.MaxMailboxDepth = 1
	mustApply(t, s, &Op{Kind: OpSendMessage, Token: "worker", To: "lead", MsgType: MsgQuestion, Body: "blocking"}, t0)
	before := s.Serial
	if _, _, err := s.Apply(&op, t0); err == nil {
		t.Fatal("setup: full mailbox did not refuse the notice")
	}
	if s.Serial != before || s.Messages[request].StallNotifiedDeclaration != 0 {
		t.Fatal("failed send recorded an already-told fact")
	}
	s.Limits.MaxMailboxDepth = 2
	result := mustApply(t, s, &op, t0.Add(time.Second))
	if s.Serial != before+1 || s.Messages[request].StallNotifiedDeclaration != version {
		t.Fatal("notice and watermark did not commit together at one serial")
	}
	if s.Messages[result["msg_serial"].(uint64)].Body != "private notice" {
		t.Fatal("watermark lacks its notice")
	}
	before = s.Serial
	mustApply(t, s, &op, t0.Add(2*time.Second))
	if s.Serial != before {
		t.Fatal("same-version retry created a second notice")
	}
}

func TestFinishedWakeNoticeCommitsOnlyWithDaemonSend(t *testing.T) {
	s := NewState("finished-wake", DefaultLimits())
	ackReg(t, s, "sender", "sender", t0)
	ackReg(t, s, "worker", "worker", t0)
	ackReg(t, s, "reporter", "reporter", t0)
	s.AgentByToken("reporter").Nonce = DibsNonce
	r := mustApply(t, s, &Op{Kind: OpSendMessage, Token: "sender", To: "worker", MsgType: MsgRequest, Body: "work"}, t0)
	source := s.Messages[r["msg_serial"].(uint64)]
	op := &Op{
		Kind: OpSendMessage, Token: "worker", To: "sender", MsgType: MsgNotify,
		Body: "finished wake", FinishedWakeOf: source.Serial,
	}
	if err := s.Admit(op); err == nil {
		t.Fatal("ordinary agent can forge a finished-wake notice")
	}
	op.Token = "reporter"
	if err := s.Admit(op); err != nil {
		t.Fatal(err)
	}
	s.Limits.MaxMailboxDepth = 1
	mustApply(t, s, &Op{Kind: OpSendMessage, Token: "worker", To: "sender", MsgType: MsgQuestion, Body: "capacity"}, t0)
	before := s.Serial
	if _, _, err := s.Apply(op, t0); err == nil || s.Serial != before || source.FinishedWakeNotice != 0 {
		t.Fatalf("refused send consumed a notice opportunity: %v", err)
	}
	s.Limits.MaxMailboxDepth = 2
	receipt := mustApply(t, s, op, t0)["msg_serial"].(uint64)
	if receipt != before+1 || source.FinishedWakeNotice != receipt || source.State != MsgStatePending {
		t.Fatalf("notice and source marker were not committed together: %+v", source)
	}
	mustApply(t, s, op, t0)
	if s.Serial != receipt {
		t.Fatal("retry created a second notice")
	}
}
