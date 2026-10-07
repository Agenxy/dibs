// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

type failingReconcileLedger struct{ entered chan struct{} }

func (l *failingReconcileLedger) Append(_ uint64, _ time.Time, op *core.Op) error {
	if op.Kind == core.OpRegister && op.Name == "fail-stop" {
		close(l.entered)
		return errors.New("fixture: ledger persistence failed")
	}
	return nil
}

func TestWriterPanicDoesNotDeadlockReconcileCompletion(t *testing.T) {
	store := newPausedReconcileStore(t)
	led := &failingReconcileLedger{entered: make(chan struct{})}
	e := New(core.NewState("reconcile-panic", core.DefaultLimits()), led, nil)
	e.SetBlobs(store)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan any, 1)
	go func() {
		defer func() { done <- recover() }()
		e.Run(ctx)
	}()
	t.Cleanup(func() { store.resume(); cancel() })
	select {
	case <-store.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("startup reconcile did not enter")
	}
	call, stopCall := context.WithCancel(ctx)
	defer stopCall()
	called := make(chan struct{})
	go func() {
		defer close(called)
		_, _ = e.Do(call, &core.Op{Kind: core.OpRegister, Name: "fail-stop"})
	}()
	select {
	case <-led.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("production ledger failure did not occur")
	}
	store.resume()
	select {
	case fault := <-done:
		if !strings.Contains(fmt.Sprint(fault), "ledger persistence failure") {
			t.Fatalf("writer did not preserve its fail-stop panic: %v", fault)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("writer panic deadlocked on a worker receipt with an uncanceled context")
	}
	stopCall()
	<-called
}
