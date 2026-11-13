// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/testcodexipc"
)

func investigationWorkers(e *Engine) int {
	e.wakeWorkers.mu.Lock()
	defer e.wakeWorkers.mu.Unlock()
	n := 0
	for _, w := range e.wakeWorkers.active {
		if w.Agent == "worker" {
			n++
		}
	}
	return n
}

func investigationAwait(t *testing.T, label string, ready func() bool) {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		if ready() {
			return
		}
		<-time.After(time.Millisecond)
	}
	t.Fatal("setup did not reach:", label)
}

// Enter through real Send, native framing and the ordinary exit defers. The
// fixture's second native discovery is held while the first worker fully exits.
// The production worker-ID table, not the suspect running bool, proves that
// exactly one real delivery remains in flight.
func TestNativeFirstExitPreservesOtherInFlightDelivery(t *testing.T) {
	t.Setenv("DIBS_DIR", t.TempDir())
	firstEntered, secondEntered := make(chan struct{}), make(chan struct{})
	firstRelease, secondRelease := make(chan struct{}), make(chan struct{})
	var armed atomic.Bool
	var calls atomic.Int32
	s := testcodexipc.Start(t, "active", func() {
		if !armed.Load() {
			return
		}
		switch calls.Add(1) {
		case 1:
			close(firstEntered)
			<-firstRelease
		case 2:
			close(secondEntered)
			<-secondRelease
		}
	})
	e, ctx, worker, sender := nativeAppEngine(t)
	defer func() {
		for _, ch := range []chan struct{}{firstRelease, secondRelease} {
			select {
			case <-ch:
			default:
				close(ch)
			}
		}
		investigationAwait(t, "all native workers really finished", func() bool { return investigationWorkers(e) == 0 })
	}()
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: worker}); err != nil {
		t.Fatal("setup contact:", err)
	}
	initial, err := e.query(ctx, func() core.Result {
		return core.Result{"live": e.contactLooksLive(e.state.Agents["worker"]), "ended": e.turnEnded["worker"]}
	})
	if err != nil || initial["live"] != true || !initial["ended"].(time.Time).IsZero() {
		t.Fatal("setup: real current contact was not live:", initial, err)
	}
	armed.Store(true)
	nativeAppSend(t, e, ctx, sender)
	select {
	case <-firstEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("setup first discovery")
	}
	nativeAppSend(t, e, ctx, sender)
	if got := investigationWorkers(e); got != 2 {
		t.Fatalf("setup: two real native workers were not admitted: %d", got)
	}
	close(firstRelease)
	select {
	case <-secondEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("setup second discovery")
	}
	investigationAwait(t, "first worker exited while second is blocked", func() bool { return investigationWorkers(e) == 1 })
	if len(s.Inputs()) != 1 {
		t.Fatalf("setup: expected exactly first input accepted, got %d", len(s.Inputs()))
	}
	state, err := e.query(context.Background(), func() core.Result {
		e.wakers.mu.Lock()
		running, arrived := e.wakers.running["worker"], e.wakers.arrived["worker"]
		e.wakers.mu.Unlock()
		return core.Result{
			"running": running, "arrived": arrived, "ended": e.turnEnded["worker"],
			"live": e.contactLooksLive(e.state.Agents["worker"]),
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("one real native worker remains, no Stop: state=%v", state)
	if state["running"] != true {
		t.Error("first native exit cleared running while second delivery is in flight")
	}
	if !state["ended"].(time.Time).IsZero() {
		t.Error("first native exit claimed a turn end without a Stop while second delivery is in flight")
	}
	if state["live"] != true {
		t.Error("first native exit made real recent contact look inactive while second delivery is in flight")
	}
}

func TestNativeFirstExitDoesNotRecheckReconnectWhileOthersRun(t *testing.T) {
	t.Setenv("DIBS_DIR", t.TempDir())
	firstEntered, secondEntered := make(chan struct{}), make(chan struct{})
	firstRelease, secondRelease := make(chan struct{}), make(chan struct{})
	var armed atomic.Bool
	var calls atomic.Int32
	testcodexipc.Start(t, "active", func() {
		if !armed.Load() {
			return
		}
		switch calls.Add(1) {
		case 1:
			close(firstEntered)
			<-firstRelease
		case 2:
			close(secondEntered)
			<-secondRelease
		}
	})
	e, ctx, _, sender := nativeAppEngine(t)
	defer func() {
		for _, ch := range []chan struct{}{firstRelease, secondRelease} {
			select {
			case <-ch:
			default:
				close(ch)
			}
		}
		investigationAwait(t, "all reconnect workers finished", func() bool { return investigationWorkers(e) == 0 })
	}()
	send := func() {
		t.Helper()
		if _, err := e.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: sender, To: "worker", MsgType: core.MsgQuestion, Body: "fixture question"}); err != nil {
			t.Fatal("setup send:", err)
		}
	}
	armed.Store(true)
	send()
	select {
	case <-firstEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("setup first discovery")
	}
	e.wakeWorkers.mu.Lock()
	if len(e.wakeWorkers.active) != 1 {
		e.wakeWorkers.mu.Unlock()
		t.Fatal("setup first worker count")
	}
	var firstID uint64
	for id := range e.wakeWorkers.active {
		firstID = id
	}
	e.wakeWorkers.mu.Unlock()
	send()
	if err := e.AppReconnected(ctx, e.HostID(), harnessenv.AppIncarnation{PID: 4242, Start: "fixture-new-app"}); err != nil {
		t.Fatal("setup real reconnect:", err)
	}
	if got := investigationWorkers(e); got != 3 {
		t.Fatalf("setup: expected two original attempts and one reconnect attempt, got %d", got)
	}
	close(firstRelease)
	select {
	case <-secondEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("setup second discovery")
	}
	investigationAwait(t, "first original worker fully exited", func() bool {
		e.wakeWorkers.mu.Lock()
		defer e.wakeWorkers.mu.Unlock()
		_, exists := e.wakeWorkers.active[firstID]
		return !exists
	})
	if got := investigationWorkers(e); got != 2 {
		t.Errorf("first native exit scheduled another reconnect re-check with two deliveries still in flight: got %d workers, want 2", got)
	}
}
