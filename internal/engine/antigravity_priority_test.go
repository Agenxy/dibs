// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/antigravity"
	"github.com/agenxy/dibs/internal/core"
)

func TestRefusedAntigravityNotifyUsesContactEligibility(t *testing.T) {
	for _, priority := range []string{"normal", "high", "urgent"} {
		t.Run(priority, func(t *testing.T) {
			entered := make(chan struct{}, 1)
			old := prepareAntigravity
			prepareAntigravity = func(context.Context, string, string) (func(context.Context, string) error, error) {
				entered <- struct{}{}
				return nil, errors.New("fixture unavailable")
			}
			t.Cleanup(func() { prepareAntigravity = old })
			e := New(core.NewState("test", core.DefaultLimits()), &memLedger{}, deadProber{})
			ctx, cancel := context.WithCancel(t.Context())
			go e.Run(ctx)
			t.Cleanup(func() { cancel(); <-e.Done() })
			const conversation = "01a10520-0000-7000-8000-000000000097"
			worker := contactReplayDo(t, e, ctx, &core.Op{
				Kind: core.OpRegister, Name: "worker", AgentKind: core.KindPersistent, SessionAlias: conversation,
				Agent: &core.AgentInfo{Harness: antigravity.Client, HostID: e.HostID()},
			})["token"].(string)
			e.BindAntigravity(ctx, worker, conversation)
			sender := registerAgent(t, e, "sender")
			sleepAntigravitySendRecipient(t, e, ctx)
			serial := contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpSendMessage, Token: sender, To: "worker", MsgType: core.MsgNotify, RequestPriority: priority, Body: "fixture"})["msg_serial"].(uint64)
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("setup: no actual preflight refusal")
			}
			waitWakeDone(t, e, "worker")
			_, err := e.query(ctx, func() core.Result {
				got := e.state.Messages[serial].ContactEscalatedAt != 0
				if want := priority != "normal"; got != want {
					t.Errorf("notify priority %s contact=%v want=%v", priority, got, want)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
