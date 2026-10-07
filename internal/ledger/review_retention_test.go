// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package ledger

import (
	"reflect"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestMixedRetentionDecisionsSurviveEncryptedLedgerReplay(t *testing.T) {
	led, path := newLedger(t)
	st := core.NewState("test", core.DefaultLimits())
	do := func(op *core.Op, at time.Time) core.Result { return apply(t, st, led, op, at) }
	do(&core.Op{Kind: core.OpRegister, Name: "lead", NewToken: "lead"}, t0)
	do(&core.Op{Kind: core.OpRegister, Name: "worker", NewToken: "worker"}, t0)
	old := do(&core.Op{Kind: core.OpSendMessage, Token: "lead", To: "worker", MsgType: core.MsgQuestion, Body: "old"}, t0)["msg_serial"].(uint64)
	do(&core.Op{Kind: core.OpRespond, Token: "worker", MsgSerial: old, Disposition: "answer"}, t0)
	do(&core.Op{Kind: core.OpSweep, PurgeMail: true, V7Semantics: true, KeepOwed: true}, t0.Add(20*time.Minute))
	if st.Messages[old] != nil {
		t.Fatal("legacy response survived its historical sweep")
	}
	now := t0.Add(time.Hour)
	newer := do(&core.Op{Kind: core.OpSendMessage, Token: "lead", To: "worker", MsgType: core.MsgQuestion, Body: "new"}, now)["msg_serial"].(uint64)
	until := now.Add(24 * time.Hour)
	do(&core.Op{Kind: core.OpRespond, Token: "worker", MsgSerial: newer, Disposition: "answer", RetainUntil: &until}, now)
	do(&core.Op{Kind: core.OpSweep, PurgeMail: true, V7Semantics: true, KeepOwed: true}, now.Add(20*time.Minute))
	got := reopen(t, path)
	if got.Messages[newer] == nil || !got.Messages[newer].RetainUntil.Equal(until) || !reflect.DeepEqual(st, got) {
		t.Fatal("encrypted replay lost the new retention decision or changed historical state")
	}
}
