// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"encoding/json"
	"testing"
)

func fyiReceiptState(t *testing.T) *State {
	t.Helper()
	st := NewState("fyi-receipts", DefaultLimits())
	for _, op := range []*Op{
		{Kind: OpRegister, Name: "sender", NewToken: "sender"},
		{Kind: OpRegister, Name: "reader", NewToken: "reader"},
		{Kind: OpSendMessage, Token: "sender", To: "reader", MsgType: MsgNotify, Body: "own FYI"},
		{Kind: OpSendMessage, Token: "reader", To: "sender", MsgType: MsgNotify, Body: "foreign FYI"},
		{Kind: OpSendMessage, Token: "sender", To: "reader", MsgType: MsgRequest, Body: "owed work"},
	} {
		if _, _, err := st.Apply(op, t0); err != nil {
			t.Fatalf("setup %s: %v", op.Kind, err)
		}
	}
	if st.Messages[3].To != "reader" || st.Messages[5].Type != MsgRequest {
		t.Fatal("setup did not create receipt subjects")
	}
	return st
}

func TestFYIReceiptAdmissionRejectsInvalidCarriersAndSubjects(t *testing.T) {
	for _, raw := range []string{
		`{"kind":"ack","msg_serial":3,"notify_consumption":"invented"}`,
		`{"kind":"activity_checkpoint","msg_serial":3,"notify_consumption":"presented"}`,
		`{"kind":"ack","msg_serial":4,"notify_consumption":"presented"}`,
		`{"kind":"ack","msg_serial":5,"notify_consumption":"presented"}`,
		`{"kind":"ack","msg_serial":900,"notify_consumption":"reminded"}`,
		`{"kind":"ack","msg_serial":3,"notify_consumption":"presented","notify_announced":[3]}`,
		`{"kind":"ack","notify_announced":[3]}`,
		`{"kind":"activity_checkpoint","notify_announced":[]}`,
		`{"kind":"activity_checkpoint","notify_announced":[3,3]}`,
		`{"kind":"activity_checkpoint","notify_announced":[4]}`,
		`{"kind":"activity_checkpoint","notify_announced":[5]}`,
		`{"kind":"activity_checkpoint","notify_announced":[3],"mailbox_serials":[]}`,
		`{"kind":"activity_checkpoint","notify_announced":[3],"contact_notice_through_serial":3}`,
	} {
		t.Run(raw, func(t *testing.T) {
			st := fyiReceiptState(t)
			var op Op
			if err := json.Unmarshal([]byte(raw), &op); err != nil {
				t.Fatal(err)
			}
			op.Token = "reader"
			before := st.Serial
			if err := st.Admit(&op); err == nil {
				t.Fatalf("invalid receipt admitted: %s", raw)
			}
			if st.Serial != before || st.Messages[3].Consumed || st.Messages[5].Consumed {
				t.Fatal("admission changed replay state")
			}
		})
	}
}

func TestFYIAnnouncementDuplicateDoesNotAdvanceSerial(t *testing.T) {
	st := fyiReceiptState(t)
	var op Op
	if err := json.Unmarshal([]byte(`{"kind":"activity_checkpoint","notify_announced":[3]}`), &op); err != nil {
		t.Fatal(err)
	}
	op.Token = "reader"
	if err := st.Admit(&op); err != nil {
		t.Fatal(err)
	}
	before := st.Serial
	if _, _, err := st.Apply(&op, t0); err != nil || st.Serial != before+1 {
		t.Fatalf("first announcement not recorded: %v", err)
	}
	before = st.Serial
	if _, _, err := st.Apply(&op, t0); err != nil || st.Serial != before {
		t.Fatalf("duplicate announcement advanced serial: %v", err)
	}
	if st.Messages[3].Consumed || st.Messages[5].Consumed {
		t.Fatal("announcement consumed mail")
	}
	if _, _, err := st.Apply(&Op{Kind: OpActivityCheckpoint, Token: "reader"}, t0); err != nil || st.Serial != before+1 {
		t.Fatalf("historical absent-field activity changed: %v", err)
	}
}
