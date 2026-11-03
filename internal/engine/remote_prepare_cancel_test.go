// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

// Capture the actual unbuffered writer submission, then cancel its caller
// before serving that admitted request. A query may still complete after its
// caller leaves: preparation must not retain a mutable alias to that worker.
func TestCanceledRemotePreparationOwnsItsOutcome(t *testing.T) {
	e := New(core.NewState("hub", core.DefaultLimits()), &memLedger{}, deadProber{})
	e.SetWakePolicy(WakeNone)
	_, detach, err := e.AttachHostBridgeCapabilities("remote", []string{"codex"}, nil, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(detach)
	// Attach submits its own reconnect query. Serve that separately so the
	// request captured below is the preparation under test, never this setup.
	select {
	case reconnect := <-e.ops:
		e.serveRequest(reconnect)
	case <-time.After(3 * time.Second):
		t.Fatal("setup: host reconnect query did not arrive")
	}
	do := func(op *core.Op) core.Result {
		t.Helper()
		r, err := e.exec(op, time.Now())
		if err != nil || r["error"] != nil {
			t.Fatalf("setup: %v %v", r, err)
		}
		return r
	}
	worker := do(&core.Op{
		Kind: core.OpRegister, Name: "worker", SessionID: remoteThread,
		Agent: &core.AgentInfo{Harness: "Codex", HostID: "remote", Surface: harnessenv.ChatGPTApp},
	})["token"].(string)
	sender := do(&core.Op{Kind: core.OpRegister, Name: "sender"})["token"].(string)
	serial := do(&core.Op{
		Kind: core.OpSendMessage, Token: sender, To: "worker",
		MsgType: core.MsgQuestion, Body: "handled after cancellation",
	})["msg_serial"].(uint64)
	plan, ok := e.wakeFor(e.state.Agents["worker"], core.MsgQuestion, questionFor("worker"))
	if !ok || plan.host != "remote" || plan.nativeOutcome == nil {
		t.Fatal("setup: no real remote plan")
	}
	do(&core.Op{Kind: core.OpUpdate, Token: worker, Agent: &core.AgentInfo{Surface: "codex"}})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	e.wakeContext = ctx
	plan.trackOffer = true
	done := make(chan bool, 1)
	go func() { done <- e.requestRemoteWakePlan(&plan, "worker", time.Second) }()
	var req request
	select {
	case req = <-e.ops:
	case <-time.After(3 * time.Second):
		t.Fatal("preparation never entered the writer channel")
	}
	cancel()
	select {
	case result := <-done:
		if result {
			t.Fatal("canceled preparation reported a delivery")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled caller did not leave")
	}
	before := *plan.nativeOutcome
	if !before.NoRetry || before.Disposition != "unknown" || before.Settled {
		t.Fatalf("canceled handoff lacks an honest unknown result: %+v", before)
	}
	do(&core.Op{
		Kind: core.OpRespond, Token: worker, MsgSerial: serial,
		Disposition: "answer", Body: "handled before admitted preparation completes",
	})
	if len(e.deliveryKeysAt("worker", time.Now())) != 0 {
		t.Fatal("setup: answered mail is still actionable")
	}
	e.serveRequest(req)
	late := <-req.reply
	prepared, found := late.res["plan"].(wakePlan)
	if late.err != nil || !found || prepared.nativeOutcome == nil || !prepared.nativeOutcome.Settled {
		t.Fatalf("setup: captured request did not complete remote preparation: %+v", late)
	}
	if after := *plan.nativeOutcome; after != before {
		t.Fatalf("late writer completion mutated the canceled worker: before=%+v after=%+v", before, after)
	}
}
