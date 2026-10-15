// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"strings"
	"testing"
	"time"
)

// gpt-k7-3's questions to k7-dev expired as "recipient is dormant; it will see
// this on wake" while k7-dev was calling inbox throughout (2026-10-05): they
// were never shown, because a page never reached them. An expiry of mail the
// recipient never saw says so, whatever the board thought of the recipient,
// and a question the recipient was shown keeps the diagnosis.
func TestAQuestionThatExpiresUnseenSaysItWasNeverShown(t *testing.T) {
	for _, dormant := range []bool{false, true} {
		s := NewState("never-shown", DefaultLimits())
		ackReg(t, s, "sender", "sender", t0)
		ackReg(t, s, "recipient", "recipient", t0)
		unseen := mustApply(t, s, &Op{Kind: OpSendMessage, Token: "sender", To: "recipient", MsgType: MsgQuestion, Body: "q1", DeadlineSec: 600}, t0)["msg_serial"].(uint64)
		seen := mustApply(t, s, &Op{Kind: OpSendMessage, Token: "sender", To: "recipient", MsgType: MsgQuestion, Body: "q2", DeadlineSec: 600}, t0)["msg_serial"].(uint64)
		mustApply(t, s, &Op{Kind: OpMarkDelivered, MsgSerials: []uint64{seen}}, t0.Add(time.Minute))
		if dormant {
			mustApply(t, s, &Op{Kind: OpSweep, StaleAgents: []string{"recipient"}}, t0.Add(2*time.Minute))
		}
		mustApply(t, s, &Op{Kind: OpSweep}, t0.Add(time.Hour))
		if d := s.Messages[unseen].ExpireDetail; !strings.Contains(d, "never shown") {
			t.Errorf("dormant=%v: an unseen question's expiry blames the recipient: %q", dormant, d)
		}
		if d := s.Messages[seen].ExpireDetail; strings.Contains(d, "never shown") || d == "" {
			t.Errorf("dormant=%v: a shown question lost its diagnosis: %q", dormant, d)
		}
	}
}
