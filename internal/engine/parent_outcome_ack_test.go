// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Historical fleets can retain sent mail after a row disappears. Enter the
// actual engine acknowledgement path with a replacement incarnation, not a
// manually authorized flag or an outcome-read setter.
func TestParentAckKeepsReplacementFence(t *testing.T) {
	now := time.Unix(1700000000, 0)
	st := core.NewState("parent-fence", core.DefaultLimits())
	apply := func(op *core.Op) core.Result {
		t.Helper()
		r, _, err := st.Apply(op, now)
		if err != nil {
			t.Fatal("setup:", err)
		}
		return r
	}
	apply(&core.Op{Kind: core.OpRegister, Name: "sender", NewToken: "old-token", V7Semantics: true})
	apply(&core.Op{Kind: core.OpRegister, Name: "worker", NewToken: "worker-token", V7Semantics: true})
	n := apply(&core.Op{Kind: core.OpSendMessage, Token: "old-token", To: "worker", MsgType: core.MsgRequest, Body: "old task", V7Semantics: true})["msg_serial"].(uint64)
	apply(&core.Op{Kind: core.OpRespond, Token: "worker-token", MsgSerial: n, Disposition: "approve", V7Semantics: true})
	// Model a legacy sweep removing an ephemeral sender while mail survives.
	delete(st.Agents, "sender")
	apply(&core.Op{Kind: core.OpRegister, Name: "sender", NewToken: "replacement-token", V7Semantics: true})
	if allowed, _ := core.MessageAccess(n, st.Messages[n], st.Agents["sender"]); allowed {
		t.Fatal("setup: replacement unexpectedly owns old sent mail")
	}
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.Run(ctx)
	_, err := e.Do(ctx, &core.Op{Kind: core.OpAckMessage, Token: "replacement-token", MsgSerial: n})
	var ce *core.Error
	if err == nil || !errors.As(err, &ce) || ce.Code != "E_NO_MESSAGE" {
		t.Fatalf("replacement acknowledged a predecessor's outcome: %v", err)
	}
}
