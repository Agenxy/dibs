// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

func TestQueuedAcceptanceDoesNotContinueButStartingDoes(t *testing.T) {
	b := newContinuationBoard(t)
	sender, err := b.e.Do(b.ctx, &core.Op{Kind: core.OpRegister, Name: "lead"})
	if err != nil {
		t.Fatal(err)
	}
	mail, err := b.e.Do(b.ctx, &core.Op{
		Kind: core.OpSendMessage, Token: sender["token"].(string),
		To: "worker", MsgType: core.MsgRequest, Body: "accepted for later",
	})
	if err != nil {
		t.Fatal(err)
	}
	n := mail["msg_serial"].(uint64)
	if _, err := b.e.Do(b.ctx, &core.Op{
		Kind: core.OpRespond, Token: b.token, MsgSerial: n, Disposition: "queue",
	}); err != nil {
		t.Fatal(err)
	}
	b.wake(t) // actual successful delivery establishes a Dibs-started turn
	if continued(b.stop(t, false)) {
		t.Fatal("queued acceptance was treated as work already started")
	}
	if _, err := b.e.Do(b.ctx, &core.Op{
		Kind: core.OpRespond, Token: b.token, MsgSerial: n, Disposition: "approve",
	}); err != nil {
		t.Fatal(err)
	}
	if !continued(b.stop(t, false)) {
		t.Fatal("starting accepted work did not establish a working obligation")
	}
}
