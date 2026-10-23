// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mailhistory_test

import (
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

func TestBulkFYIConsumptionAppearsInNativeHistory(t *testing.T) {
	f := nativeHistory(t, t.TempDir())
	sender := historyIdentity(t, f, "sender")
	reader := historyIdentity(t, f, "reader")
	sent := historyOp(t, f, &core.Op{Kind: core.OpSendMessage, Token: sender, To: "reader", MsgType: core.MsgNotify, Body: strings.Repeat("large FYI ", 2500)})
	serial := sent["msg_serial"].(uint64)
	// A full oversized result is explicitly announced, never falsely consumed.
	if _, err := f.eng.GetMessage(f.ctx, reader, serial); err != nil {
		t.Fatal(err)
	}
	r := historyOp(t, f, &core.Op{Kind: core.OpAckMessage, Token: reader, NotifyThroughSerial: serial})
	if r["consumed_count"] != 1 {
		t.Fatalf("setup: bulk failed: %v", r)
	}
	page := settledHistory(t, f, reader, false)
	if !strings.Contains(historyJSON(t, page), `"consumed":true`) {
		t.Fatalf("bulk change absent from native history: %v", page)
	}
}
