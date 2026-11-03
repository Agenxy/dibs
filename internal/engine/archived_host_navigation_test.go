// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/testcodexipc"
)

func TestArchivedRemoteAppPlansCannotReachAnOldOpeningBridge(t *testing.T) {
	for _, capable := range []bool{false, true} {
		t.Run(map[bool]string{false: "old", true: "native-only"}[capable], func(t *testing.T) {
			e := New(core.NewState("hub", core.DefaultLimits()), &memLedger{}, deadProber{})
			caps := []bool{true}
			if capable {
				caps = append(caps, true)
			}
			wakes, release, err := e.AttachHostBridgeCapabilities("remote", []string{"codex"}, nil, 0, caps...)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(release)
			ctx, cancel := context.WithCancel(context.Background())
			joined := make(chan struct{})
			go func() { e.Run(ctx); close(joined) }()
			t.Cleanup(func() { cancel(); <-joined })
			stopWakeTimersOnCleanup(t, e)
			do := func(op *core.Op) core.Result {
				t.Helper()
				r, err := e.Do(ctx, op)
				if err != nil || r["error"] != nil {
					t.Fatalf("setup: %v %v", r, err)
				}
				return r
			}
			do(&core.Op{Kind: core.OpRegister, Name: "worker", SessionID: testcodexipc.Thread, Agent: &core.AgentInfo{Harness: "Codex", Surface: harnessenv.ChatGPTApp, HostID: "remote"}})
			sender := do(&core.Op{Kind: core.OpRegister, Name: "sender"})["token"].(string)
			if _, err = e.query(ctx, func() core.Result { e.state.Agents["worker"].Status = core.StatusArchived; return nil }); err != nil {
				t.Fatal(err)
			}
			do(&core.Op{Kind: core.OpSendMessage, Token: sender, To: "worker", MsgType: core.MsgNotify, Body: "retained remote mail"})
			captured := make(chan WakeRequest, 1)
			receiverDone := make(chan struct{})
			go func() {
				defer close(receiverDone)
				select {
				case request := <-wakes:
					captured <- request
					e.ReportWakeResult(WakeResult{ID: request.ID, Host: "remote", OK: true, NativeDelivery: "accepted"})
				case <-ctx.Done():
				}
			}()
			t.Cleanup(func() { cancel(); <-receiverDone })
			waitWakeDone(t, e, "worker")
			select {
			case request := <-captured:
				if !capable {
					t.Fatal("archived app plan reached an older bridge capable of navigation")
				}
				raw, err := json.Marshal(request)
				if err != nil {
					t.Fatal(err)
				}
				var wire map[string]any
				if err = json.Unmarshal(raw, &wire); err != nil {
					t.Fatal(err)
				}
				if wire["no_navigation"] != true {
					t.Fatalf("capable bridge received an open-capable plan: %s", raw)
				}
			default:
				if capable {
					t.Fatal("capable bridge did not receive native-only delivery")
				}
			}
		})
	}
}
