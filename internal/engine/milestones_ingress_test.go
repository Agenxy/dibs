// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// claude-agenxy-labs-0, 2026-10-11: approving request 89566 with the three
// milestones its sender had already named bounced, and the second attempt,
// without them, worked. Restating the sender's steps is agreement. A
// different list is still refused, with the steps quoted and the fix named.
func TestApprovingWithTheRequestsOwnMilestonesIsAgreementNotAConflict(t *testing.T) {
	e := New(core.NewState("milestones", core.DefaultLimits()), &memLedger{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	do := func(op *core.Op) (core.Result, error) { return e.Do(ctx, op) }
	tokens := map[string]string{}
	for _, id := range []string{"lead", "worker"} {
		r, err := do(&core.Op{Kind: core.OpRegister, Name: id, Nonce: "milestones-" + id})
		if err != nil {
			t.Fatal("setup:", err)
		}
		tokens[id] = r["token"].(string)
		if _, err := do(&core.Op{Kind: core.OpAckBoard, Token: tokens[id]}); err != nil {
			t.Fatal("setup:", err)
		}
	}
	steps := []string{"reserve disk", "copy images", "verify"}
	send := func() uint64 {
		r, err := do(&core.Op{
			Kind: core.OpSendMessage, Token: tokens["lead"], To: "worker",
			MsgType: core.MsgRequest, Body: "make room", Milestones: steps,
		})
		if err != nil {
			t.Fatal("setup send:", err)
		}
		return r["msg_serial"].(uint64)
	}

	same := send()
	if _, err := do(&core.Op{
		Kind: core.OpRespond, Token: tokens["worker"], MsgSerial: same,
		Disposition: "approve", Milestones: append([]string(nil), steps...),
	}); err != nil {
		t.Fatalf("approving with the request's own milestones was refused: %v", err)
	}
	got, _ := e.query(ctx, func() core.Result { return core.Result{"m": e.state.Messages[same].Milestones} })
	if !slices.Equal(got["m"].([]string), steps) {
		t.Fatalf("milestones changed by a restating approval: %q", got["m"])
	}

	other := send()
	_, err := do(&core.Op{
		Kind: core.OpRespond, Token: tokens["worker"], MsgSerial: other,
		Disposition: "approve", Milestones: []string{"something else"},
	})
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Code != "E_BAD_ARG" || !strings.Contains(ce.Hint, "omit milestones") ||
		!strings.Contains(ce.Hint, "copy images") {
		t.Fatalf("a different list must be refused with the steps and the fix: %v", err)
	}
}
