// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/testcodexipc"
)

func nativeAppEngine(t *testing.T) (*Engine, context.Context, string, string) {
	t.Helper()
	e := New(core.NewState("native-app", core.DefaultLimits()), &memLedger{}, deadProber{})
	// There is deliberately no executable at this canonical queue path. Native
	// success must never reach a queue command, observer, receipt lock or opener.
	e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{
		filepath.Join(t.TempDir(), "codex"), "queue", "--thread", "{thread}", "--message", "{message}",
	}}})
	ctx, cancel := context.WithCancel(context.Background())
	joined := make(chan struct{})
	go func() { e.Run(ctx); close(joined) }()
	t.Cleanup(func() { waitWakeDone(t, e, "worker"); cancel(); <-joined })
	stopWakeTimersOnCleanup(t, e)
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
		Agent: &core.AgentInfo{Harness: "Codex", Surface: harnessenv.ChatGPTApp},
	})["token"].(string)
	sender := do(&core.Op{Kind: core.OpRegister, Name: "sender"})["token"].(string)
	return e, ctx, worker, sender
}

func nativeAppSend(t *testing.T, e *Engine, ctx context.Context, sender string) uint64 {
	t.Helper()
	r, err := e.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: sender, To: "worker", MsgType: core.MsgNotify, Body: "private body marker"})
	if err != nil {
		t.Fatal("setup send:", err)
	}
	serial, ok := r["msg_serial"].(uint64)
	if !ok {
		t.Fatalf("setup send did not apply: %v", r)
	}
	return serial
}

func awaitNative(t *testing.T, s *testcodexipc.Server, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(s.Inputs()) >= n {
			return
		}
		<-time.After(5 * time.Millisecond)
	}
	t.Fatalf("native inputs %d, want %d", len(s.Inputs()), n)
}

func TestNativeAppDeliveryEntersThroughSend(t *testing.T) {
	for _, mode := range []string{"idle", "active"} {
		t.Run(mode, func(t *testing.T) {
			s := testcodexipc.Start(t, mode, nil)
			e, ctx, _, sender := nativeAppEngine(t)
			nativeAppSend(t, e, ctx, sender)
			awaitNative(t, s, 1)
			waitWakeDone(t, e, "worker")
			calls := s.Inputs()
			if len(calls) != 1 {
				t.Fatalf("duplicate native input: %d", len(calls))
			}
			p := calls[0]["params"].(map[string]any)["turnStart"].(map[string]any)["request"].(map[string]any)
			text := p["input"].([]any)[0].(map[string]any)["text"]
			if text != "Dibs: new notify from sender." {
				t.Fatalf("notice leaked body or changed facts: %v", text)
			}
			note := e.PullOnlyNoteFor(ctx, "worker")
			if strings.Contains(note, "delivered") || !strings.Contains(note, "Last native app notice:") {
				t.Fatalf("dishonest note: %s", note)
			}
		})
	}
}

func TestNativeAppCancelsAfterDiscoveryWhenMailHandledOrIdentityClosed(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(map[bool]string{false: "acked", true: "closed"}[closed], func(t *testing.T) {
			ready, done := make(chan struct{}), make(chan error, 1)
			var e *Engine
			var ctx context.Context
			var worker string
			var serial uint64
			s := testcodexipc.Start(t, "idle", func() {
				<-ready
				op := &core.Op{Kind: core.OpAckMessage, Token: worker, MsgSerial: serial}
				if closed {
					op = &core.Op{Kind: core.OpSignOff, Token: worker}
				}
				_, err := e.Do(ctx, op)
				done <- err
			})
			var sender string
			e, ctx, worker, sender = nativeAppEngine(t)
			serial = nativeAppSend(t, e, ctx, sender)
			close(ready)
			select {
			case err := <-done:
				if err != nil {
					t.Fatal("setup mutation:", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("did not reach native owner discovery")
			}
			waitWakeDone(t, e, "worker")
			if len(s.Inputs()) != 0 {
				t.Fatal("stale or closed identity still woke")
			}
		})
	}
}

func TestNativeAppUnknownDoesNotArmAutomaticRetry(t *testing.T) {
	s := testcodexipc.Start(t, "disconnect", nil)
	e, ctx, _, sender := nativeAppEngine(t)
	nativeAppSend(t, e, ctx, sender)
	awaitNative(t, s, 1)
	waitWakeDone(t, e, "worker")
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	if len(e.wakers.deferred) != 0 || len(e.wakers.failedCauses) != 0 {
		t.Fatal("unknown native submission armed another delivery")
	}
}

func TestNativeAppUnknownReoffersOnceOnNewAppIncarnation(t *testing.T) {
	t.Setenv("DIBS_DIR", t.TempDir())
	s := testcodexipc.Start(t, "disconnect", nil)
	e, ctx, _, sender := nativeAppEngine(t)
	nativeAppSend(t, e, ctx, sender)
	awaitNative(t, s, 1)
	waitWakeDone(t, e, "worker")
	app := harnessenv.AppIncarnation{PID: 4242, Start: "new-fixture-app"}
	if err := e.AppReconnected(ctx, e.HostID(), app); err != nil {
		t.Fatal("setup reconnect:", err)
	}
	awaitNative(t, s, 2)
	waitWakeDone(t, e, "worker")
	if err := e.AppReconnected(ctx, e.HostID(), app); err != nil {
		t.Fatal("setup repeated reconnect:", err)
	}
	if got := len(s.Inputs()); got != 2 {
		t.Fatalf("same receiving incarnation repeated input: %d", got)
	}
	nativeAppSend(t, e, ctx, sender)
	awaitNative(t, s, 3)
}

func TestNativeAppEachArrivalBypassesBatchAndInFlightHold(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	s := testcodexipc.Start(t, "active", func() { once.Do(func() { close(entered); <-release }) })
	e, ctx, _, sender := nativeAppEngine(t)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	nativeAppSend(t, e, ctx, sender)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first input never reached owner")
	}
	nativeAppSend(t, e, ctx, sender)
	r, err := e.query(ctx, func() core.Result {
		e.wakers.mu.Lock()
		defer e.wakers.mu.Unlock()
		return core.Result{"batch": e.wakeBursts["worker"] != nil, "held": e.wakers.arrived["worker"]}
	})
	if err != nil || r["batch"] != false || r["held"] != false {
		t.Fatalf("new arrival was delayed by Dibs: %v %v", r, err)
	}
	close(release)
	awaitNative(t, s, 2)
}
