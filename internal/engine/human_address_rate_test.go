// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

func TestRateRefusedHumanSendDoesNotCreateAMailbox(t *testing.T) {
	e := New(core.NewState("human-rate", core.DefaultLimits()), &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.Run(ctx)
	r, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "sender", Nonce: "human-rate-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	token, ok := r["token"].(string)
	if !ok {
		t.Fatalf("register setup: %v", r)
	}
	refused := false
	for range 100 {
		_, err = e.EventsSince(ctx, token, 0, false)
		if errors.Is(err, core.ErrRateLimited) {
			refused = true
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if !refused {
		t.Fatal("setup: real rate bucket was never exhausted")
	}
	_, err = e.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: token, To: "human", MsgType: core.MsgRequest, Body: "approve?"})
	if !errors.Is(err, core.ErrRateLimited) {
		t.Fatalf("send was not rate-refused: %v", err)
	}
	if got := e.HumanIdentity(); got != "" {
		t.Fatalf("rate-refused send registered the person: %q", got)
	}
}
