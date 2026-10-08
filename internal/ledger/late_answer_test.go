// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package ledger

import (
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Historical writers on a sleeping Mac accepted this answer while the sweep
// recorded a wall time past the deadline. Do not patch that history: the normal
// encrypted Append -> cold Replay path must accept the original op sequence.
func TestEncryptedLedgerReplaysHistoricallyAcceptedLateAnswer(t *testing.T) {
	led, path := newLedger(t)
	live := core.NewState("test", core.DefaultLimits())
	for _, name := range []string{"sender", "recipient"} {
		apply(t, live, led, &core.Op{Kind: core.OpRegister, Name: name, NewToken: name}, t0)
		apply(t, live, led, &core.Op{Kind: core.OpAckBoard, Token: name}, t0)
	}
	q := apply(t, live, led, &core.Op{Kind: core.OpSendMessage, Token: "sender", To: "recipient", MsgType: core.MsgQuestion, Body: "question", DeadlineSec: 3600}, t0)["msg_serial"].(uint64)
	// The old live sweep changed the sender's liveness, so it was ledgered,
	// but its suspended monotonic clock did not expire the question. On cold
	// replay it also expires the question from the recorded wall timestamp.
	if err := led.Append(live.Serial+1, t0.Add(2*time.Hour), &core.Op{Kind: core.OpSweep, StaleAgents: []string{"sender"}}); err != nil {
		t.Fatal(err)
	}
	if err := led.Append(live.Serial+2, t0.Add(2*time.Hour+time.Minute), &core.Op{Kind: core.OpRespond, AgentID: "recipient", MsgSerial: q, Disposition: "answer", Body: "accepted before restart"}); err != nil {
		t.Fatal(err)
	}
	if err := led.Close(); err != nil {
		t.Fatal(err)
	}
	fold := reopen(t, path)
	m := fold.Messages[q]
	if fold.Serial != live.Serial+2 || m == nil || m.State != core.MsgStateAnswered || m.Response != "accepted before restart" || m.ExpireDetail != "" || !m.Consumed {
		t.Fatal("encrypted cold replay lost an answer the historical writer accepted")
	}
}
