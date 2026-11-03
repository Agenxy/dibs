// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestContactBoardIsDetachedAcrossLaterWriterOps(t *testing.T) {
	for _, change := range []string{"coalesce", "notified", "resolved"} {
		t.Run(change, func(t *testing.T) {
			s := NewState("snapshot", DefaultLimits())
			now := time.Unix(1700000000, 0)
			ackReg(t, s, "sender", "sender", now)
			ackReg(t, s, "recipient", "recipient", now)
			mail := mustApply(t, s, &Op{Kind: OpSendMessage, Token: "sender", To: "recipient", MsgType: MsgQuestion, Body: "first"}, now)["msg_serial"].(uint64)
			serial := mustApply(t, s, &Op{Kind: OpContactEscalate, MsgSerial: mail}, now)["contact_serial"].(uint64)
			board := s.Board()
			before, err := json.Marshal(board)
			if err != nil {
				t.Fatal(err)
			}
			if change == "coalesce" {
				next := mustApply(t, s, &Op{Kind: OpSendMessage, Token: "sender", To: "recipient", MsgType: MsgQuestion, Body: "second"}, now.Add(time.Second))["msg_serial"].(uint64)
				mustApply(t, s, &Op{Kind: OpContactEscalate, MsgSerial: next}, now.Add(time.Second))
				if s.Contacts[serial].Count != 2 {
					t.Fatal("setup did not coalesce contact")
				}
			} else {
				kind := OpContactNotified
				if change == "resolved" {
					kind = OpContactResolved
				}
				mustApply(t, s, &Op{Kind: kind, ContactSerial: serial}, now.Add(time.Second))
				if change == "notified" && s.Contacts[serial].NotifiedAt.IsZero() {
					t.Fatal("setup did not record notification")
				}
				if change == "resolved" && s.Contacts[serial].ResolvedAt.IsZero() {
					t.Fatal("setup did not resolve contact")
				}
			}
			after, err := json.Marshal(board)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("board result changed after writer mutated contact state")
			}
		})
	}
}
