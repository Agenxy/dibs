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
)

// Real publication starts the arrival timer. Hold the production writer past
// the former enqueue deadline, then require both the owed wake and a later
// original message to reach an actual command. No direct timer/helper call.
func TestWakeBurstSurvivesWriterStallAndOffersLaterMail(t *testing.T) {
	e := New(core.NewState("burst-stall", core.DefaultLimits()), &memLedger{}, deadProber{})
	out := filepath.Join(t.TempDir(), "deliveries")
	e.SetWakeCommands(map[string]WakeCommand{"codex": {
		Argv: []string{os.Args[0], "-test.run=^TestSuccessfulWakeCommandHelper$", "--", out},
	}})
	ctx, cancel := context.WithCancel(context.Background())
	joined := make(chan struct{})
	go func() { e.Run(ctx); close(joined) }()
	t.Cleanup(func() { cancel(); <-joined })
	do := func(op *core.Op) core.Result {
		t.Helper()
		r, err := e.Do(ctx, op)
		if err != nil {
			t.Fatal("setup ingress:", err)
		}
		return r
	}
	do(&core.Op{Kind: core.OpRegister, Name: "worker", SessionID: "01a0696b-8446-7821-a992-9dc7f6a43a26", Agent: &core.AgentInfo{Harness: "Codex"}})
	sender := do(&core.Op{Kind: core.OpRegister, Name: "sender"})["token"].(string)
	do(&core.Op{Kind: core.OpSendMessage, Token: sender, To: "worker", MsgType: core.MsgQuestion, Body: "owed before stall"})
	entered, release, settled := make(chan bool, 1), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := e.query(ctx, func() core.Result {
			entered <- e.wakeBursts["worker"] != nil
			<-release
			return nil
		})
		settled <- err
	}()
	// Cleanup must release a writer even when the setup assertion fails.
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	select {
	case pending := <-entered:
		if !pending {
			t.Fatal("setup: writer did not stall while an actual arrival batch was pending")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("setup: writer stall was not accepted")
	}
	<-time.After(6 * time.Second) // fixed 200 ms batch plus the former 5 s deadline
	close(release)
	if err := <-settled; err != nil {
		t.Fatal("setup: writer did not recover:", err)
	}
	await := func(attempt int) {
		t.Helper()
		deadline := time.Now().Add(4 * time.Second)
		var r core.Result
		var b []byte
		for time.Now().Before(deadline) {
			var err error
			r, err = e.query(ctx, func() core.Result {
				e.wakers.mu.Lock()
				defer e.wakers.mu.Unlock()
				return core.Result{"running": e.wakers.running["worker"], "batched": e.wakeBursts["worker"] != nil}
			})
			if err != nil {
				t.Fatal(err)
			}
			b, err = os.ReadFile(out)
			if err == nil && len(b) == attempt && r["running"] == false && r["batched"] == false {
				return
			}
			if err != nil && !os.IsNotExist(err) {
				t.Fatal("reading actual command result:", err)
			}
			<-time.After(10 * time.Millisecond)
		}
		t.Fatalf("writer recovery stranded wake %d: writes=%q state=%v", attempt, b, r)
	}
	await(1)
	do(&core.Op{Kind: core.OpSendMessage, Token: sender, To: "worker", MsgType: core.MsgQuestion, Body: "fresh after recovery"})
	await(2)
}
