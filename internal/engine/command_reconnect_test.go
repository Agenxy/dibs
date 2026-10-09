// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

// Drive the reconnect service while an actual child is held. The native bridge
// ancestry door is independently exercised by the unchanged CLI restart suite.
func TestAppReconnectFencesAnEarlierCommandsReceipt(t *testing.T) {
	(&fakeApp{holds: true}).install(t)
	e := New(core.NewState("reconnect-command", core.DefaultLimits()), &memLedger{}, deadProber{})
	e.SetHostID("fixture-host")
	out, release := filepath.Join(t.TempDir(), "deliveries"), filepath.Join(t.TempDir(), "release")
	e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{os.Args[0], "-test.run=^TestCommandItemReceiptHelper$", "--", out, release}}})
	ctx, cancel := context.WithCancel(context.Background())
	joined := make(chan struct{})
	go func() { e.Run(ctx); close(joined) }()
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600); waitWakeDone(t, e, "worker"); cancel(); <-joined })
	do := func(op *core.Op) core.Result {
		t.Helper()
		r, err := e.Do(ctx, op)
		if err != nil {
			t.Fatal("setup ingress:", err)
		}
		return r
	}
	do(&core.Op{Kind: core.OpRegister, Name: "worker", SessionID: "01a0696b-8446-7821-a992-9dc7f6a43a28", Agent: &core.AgentInfo{Harness: "Codex", Surface: harnessenv.ChatGPTApp}})
	sender := do(&core.Op{Kind: core.OpRegister, Name: "sender"})["token"].(string)
	mail := do(&core.Op{Kind: core.OpSendMessage, Token: sender, To: "worker", MsgType: core.MsgQuestion, Body: "must survive app restart"})
	if _, ok := mail["msg_serial"].(uint64); !ok {
		t.Fatalf("setup mail not accepted: %v", mail)
	}
	await := func(label, want string, settled bool) {
		t.Helper()
		until := time.Now().Add(6 * time.Second)
		var b []byte
		for time.Now().Before(until) {
			r, err := e.query(ctx, func() core.Result {
				e.wakers.mu.Lock()
				defer e.wakers.mu.Unlock()
				return core.Result{"running": e.wakers.running["worker"], "batched": e.wakeBursts["worker"] != nil}
			})
			if err != nil {
				t.Fatal(err)
			}
			b, err = os.ReadFile(out)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if string(b) == want && (!settled || (r["running"] == false && r["batched"] == false)) {
				return
			}
			<-time.After(5 * time.Millisecond)
		}
		t.Fatalf("%s: child receipts=%q want=%q", label, b, want)
	}
	await("first command held", "x", false)
	app := harnessenv.AppIncarnation{PID: 4242, Start: "observed-next-generation"}
	if err := e.AppReconnected(ctx, "fixture-host", app); err != nil {
		t.Fatal("reconnect:", err)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	await("earlier command cannot spend replacement app recovery", "xx", true)
	if err := e.AppReconnected(ctx, "fixture-host", app); err != nil {
		t.Fatal(err)
	}
	await("same incarnation is not another recovery", "xx", true)
	r, err := e.query(ctx, func() core.Result { return core.Result{"unread": len(e.state.Inbox("worker"))} })
	if err != nil || r["unread"] != 1 {
		t.Fatalf("reconnect consumed raw mail: %v %v", r, err)
	}
}
