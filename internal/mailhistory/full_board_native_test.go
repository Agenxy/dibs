// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mailhistory_test

import (
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Exercise the real register -> native writer observer -> party history path.
// A captured victim setter would miss a register classified as mail-free.
func TestFullBoardDisplacementHistoryMatchesLiveAndColdReplay(t *testing.T) {
	t.Setenv("DIBS_NOTIFY", "off")
	limits := core.DefaultLimits()
	limits.MaxAgents, limits.MaxPersistentAgents = 2, 2
	dir := t.TempDir()
	f := nativeHistoryWithLifetime(t, dir, limits, 30*time.Second)
	_ = historyIdentity(t, f, "victim")
	sender := historyIdentity(t, f, "sender")
	historyOp(t, f, &core.Op{
		Kind: core.OpSendMessage, Token: sender,
		To: "victim", MsgType: core.MsgQuestion, Body: "retained displacement question",
	})
	before := historyJSON(t, settledHistory(t, f, sender, true))
	if !strings.Contains(before, "retained displacement question") || !strings.Contains(before, core.MsgStatePending) {
		t.Fatalf("setup: pending question missing from history: %s", before)
	}
	historyOp(t, f, &core.Op{Kind: core.OpSweep, DeadAgents: []string{"victim"}})
	op := &core.Op{Kind: core.OpRegister, Name: "newcomer", Nonce: "newcomer-nonce"}
	historyOp(t, f, op)
	if op.Displaced == nil || op.Displaced.ID != "victim" {
		t.Fatalf("setup: registration did not displace victim: %+v", op.Displaced)
	}
	live := historyJSON(t, settledHistory(t, f, sender, true))
	if !strings.Contains(live, core.MsgStateExpiredDead) {
		t.Fatalf("live history missed displacement expiry: %s", live)
	}
	f.stop()
	cold := nativeHistoryWithLifetime(t, dir, limits, 30*time.Second)
	cold.ids[sender] = f.ids[sender]
	replayed := historyJSON(t, settledHistory(t, cold, sender, true))
	if live != replayed {
		t.Fatalf("native displacement history differs after cold replay:\nlive %s\ncold %s", live, replayed)
	}
}
