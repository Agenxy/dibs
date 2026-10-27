// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// Engine.Do is the writer entry used by MCP. Even an internal caller that can
// populate fields absent from the tool schema cannot assert an adoption receipt.
func TestAdoptionReceiptIsSelectedByWriter(t *testing.T) {
	f := newAdoptionFixture(t, "2026-07-28", t.TempDir())
	request := f.call("send", map[string]any{"token": f.heir, "to": "lead", "type": "request", "adopt": "lost", "body": "recover"})["msg_serial"].(float64)
	f.abandon()
	op := &core.Op{Kind: core.OpRespond, Token: f.lead, MsgSerial: uint64(request), Disposition: "approve", AdoptionRecovery: true, AdoptAuthorised: true, PriorAdoptionSerial: 999999}
	_, err := f.eng.Do(context.Background(), op)
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Code != "E_NOTHING_TO_ADOPT" || op.PriorAdoptionSerial != 0 {
		t.Fatalf("ingress trusted an invented adoption: %v; receipt %d", err, op.PriorAdoptionSerial)
	}
	f.call("send", map[string]any{"token": f.sender, "to": "lost", "type": "request", "body": "mail"})
	adopted := f.call("adopt_agent", map[string]any{"token": f.lead, "agent": "lost", "into": "heir"})
	op.PriorAdoptionSerial = 999999
	res, err := f.eng.Do(context.Background(), op)
	if err != nil || res["prior_adoption_serial"] != uint64(adopted["serial"].(float64)) || res["messages"] != 0 {
		t.Fatalf("writer did not replace the asserted receipt with its own evidence: %v %v", res, err)
	}
}
