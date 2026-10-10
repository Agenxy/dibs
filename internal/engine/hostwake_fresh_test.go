// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/testcodexipc"
)

func TestHostWakeFenceUsesOriginalMailAndIdentity(t *testing.T) {
	e := New(core.NewState("host-native", core.DefaultLimits()), &memLedger{}, deadProber{})
	wakes, release, err := e.AttachHostBridge("remote-host", []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	ctx, cancel := context.WithCancel(context.Background())
	joined := make(chan struct{})
	go func() { e.Run(ctx); close(joined) }()
	t.Cleanup(func() { cancel(); <-joined })
	do := func(op *core.Op) core.Result {
		t.Helper()
		r, err := e.Do(ctx, op)
		if err != nil {
			t.Fatal("setup:", err)
		}
		return r
	}
	worker := do(&core.Op{
		Kind: core.OpRegister, Name: "worker", SessionID: testcodexipc.Thread,
		Agent: &core.AgentInfo{Harness: "Codex", Surface: harnessenv.ChatGPTApp, HostID: "remote-host"},
	})["token"].(string)
	sender := do(&core.Op{Kind: core.OpRegister, Name: "sender"})["token"].(string)
	sent := do(&core.Op{Kind: core.OpSendMessage, Token: sender, To: "worker", MsgType: core.MsgNotify, Body: "original"})
	var wr WakeRequest
	select {
	case wr = <-wakes:
	case <-time.After(3 * time.Second):
		t.Fatal("no actual bridge request")
	}
	for _, host := range []string{"another-host", "remote-host"} {
		owed, err := e.HostWakeOwed(ctx, wr.ID, host)
		if (host == "remote-host" && err != nil) || (host != "remote-host" && !errors.Is(err, ErrWakeFenceDenied)) ||
			owed != (host == "remote-host") {
			t.Fatalf("host fence %s: %v %v", host, owed, err)
		}
	}
	do(&core.Op{Kind: core.OpAckMessage, Token: worker, MsgSerial: sent["msg_serial"].(uint64)})
	owed, err := e.HostWakeOwed(ctx, wr.ID, "remote-host")
	if err != nil || owed {
		t.Fatalf("handled mail still qualifies: %v %v", owed, err)
	}
	if !e.ReportWakeResult(WakeResult{ID: wr.ID, Host: "remote-host", OK: true, NativeDelivery: "settled"}) {
		t.Fatal("setup report refused")
	}
	waitWakeDone(t, e, "worker")
}
