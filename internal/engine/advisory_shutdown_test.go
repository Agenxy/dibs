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

type finalAdviceLedger struct {
	memLedger
	cancel  context.CancelFunc
	entered chan struct{}
}

func (l *finalAdviceLedger) Append(serial uint64, at time.Time, op *core.Op) error {
	if op.Kind == core.OpRegister && op.Name == "fail-stop-final-advice" {
		l.cancel()
		close(l.entered)
		return errors.New("fixture: final writer persistence failed")
	}
	return l.memLedger.Append(serial, at, op)
}

func TestFinalSweepAdvisoryDoesNotWaitForAStoppedWriter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	led := &finalAdviceLedger{cancel: cancel, entered: make(chan struct{})}
	e := New(core.NewState("final-advice", core.DefaultLimits()), led, deadProber{})
	fault := make(chan any, 1)
	go func() {
		defer func() { fault <- recover() }()
		e.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-e.Done():
		case <-time.After(7 * time.Second):
			t.Error("advisory worker did not finish cleanup")
		}
	})
	// Hold ordinary ticks until this advice has passed its normal coalescing
	// deadline. No test sets the sender's readiness or completion flags.
	finish := e.BeginAdvisoryBatch()
	defer finish()
	advice := Advisory{Key: "final-repo", Revision: "unchanged", What: "final tick", Remedy: "configure scorer"}
	e.QueueAdvisory(advice)
	setup, doneSetup := context.WithTimeout(context.Background(), 5*time.Second)
	defer doneSetup()
	for {
		e.advisories.mu.Lock()
		ready := !time.Now().Before(e.advisories.readyAt)
		e.advisories.mu.Unlock()
		if ready {
			break
		}
		select {
		case <-time.After(time.Millisecond):
		case <-setup.Done():
			t.Fatal("setup: advice never reached its coalescing deadline")
		}
	}
	// The real sweep starts the sender during this final writer turn. Its
	// first query cannot be admitted while this turn still executes. A real
	// ledger refusal cancels the context and enters fail-stop unwinding, so
	// the canceled select cannot randomly admit the pending query instead.
	// This is the same persistence-failure door as the reconcile exit guard.
	started := make(chan bool, 1)
	queried := make(chan struct{})
	go func() {
		defer close(queried)
		_, _ = e.query(setup, func() core.Result {
			finish()
			e.sweep(time.Now())
			e.advisories.mu.Lock()
			started <- e.advisories.flushing
			e.advisories.mu.Unlock()
			_, _ = e.exec(&core.Op{Kind: core.OpRegister, Name: "fail-stop-final-advice"}, time.Now())
			return nil
		})
	}()
	t.Cleanup(func() { doneSetup(); <-queried })
	select {
	case value := <-started:
		if !value {
			t.Fatal("setup: final sweep did not start its sender")
		}
	case <-setup.Done():
		t.Fatal("setup: final sweep did not run")
	}
	select {
	case <-led.entered:
	case <-setup.Done():
		t.Fatal("setup: writer did not enter persistence failure")
	}
	select {
	case <-e.Done():
	case <-time.After(2 * time.Second):
		t.Error("Run waited more than 2s for an advisory query against its stopped writer")
		select {
		case <-e.Done(): // join even the old five-second query before reading state
		case <-time.After(6 * time.Second):
			t.Fatal("advisory query did not finish after its original timeout")
		}
	}
	if got := <-fault; !strings.Contains(fmt.Sprint(got), "ledger persistence failure") {
		t.Fatalf("writer lost its fail-stop result: %v", got)
	}
	if len(e.state.Messages) != 0 {
		t.Fatalf("unadmitted advice became mail: %v", e.state.Messages)
	}
	e.advisories.mu.Lock()
	pending, seen := e.advisories.pending[advice.Key], e.advisories.seen[advice.Key]
	e.advisories.mu.Unlock()
	if pending != advice || seen != "" {
		t.Fatalf("shutdown spent uncommitted advice: pending=%+v seen=%q", pending, seen)
	}
}

func TestStoppedWriterRefusesUnadmittedRequests(t *testing.T) {
	e := New(core.NewState("stopped-query", core.DefaultLimits()), &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	go e.Run(ctx)
	setup, stopSetup := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopSetup()
	defer cancel()
	if _, err := e.query(setup, func() core.Result { return nil }); err != nil {
		t.Fatal("setup: writer never served its first request:", err)
	}
	cancel()
	select {
	case <-e.Done():
	case <-setup.Done():
		t.Fatal("setup: ordinary cancellation did not stop the engine")
	}
	request, stopRequest := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopRequest()
	started := time.Now()
	_, err := e.query(request, func() core.Result {
		t.Error("stopped writer admitted a new request")
		return nil
	})
	if !errors.Is(err, context.Canceled) || time.Since(started) >= 2*time.Second {
		t.Fatalf("post-shutdown query was not refused promptly: %v after %s", err, time.Since(started))
	}
}
