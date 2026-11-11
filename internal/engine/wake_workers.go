// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"cmp"
	"log/slog"
	"slices"
	"sync"
	"time"
)

// Leave room for HTTP shutdown within our 20-second service-stop budget; the
// platform's ExitTimeOut default is system-defined. Native opener deadlines can
// exceed this grace, so cancellation alone is insufficient.
const wakeShutdownGrace = 10 * time.Second

type deliveryWorker struct {
	ID      uint64
	Agent   string
	Attempt string
}

type deliveryWorkers struct {
	mu       sync.Mutex
	next     uint64
	active   map[uint64]deliveryWorker
	stopping bool
	done     chan struct{}
}

// Only the writer starts delivery workers. Once its loop has stopped, no new
// worker can be admitted while shutdown waits for this fixed set to finish.
func (e *Engine) startWakeWorker(agent, attempt string) func() {
	w := &e.wakeWorkers
	w.mu.Lock()
	w.next++
	id := w.next
	if w.active == nil {
		w.active = map[uint64]deliveryWorker{}
	}
	w.active[id] = deliveryWorker{id, agent, attempt}
	w.mu.Unlock()
	return func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		delete(w.active, id)
		if w.stopping && len(w.active) == 0 {
			close(w.done)
		}
	}
}

func (e *Engine) waitWakeWorkers() {
	w := &e.wakeWorkers
	w.mu.Lock()
	if len(w.active) == 0 {
		w.mu.Unlock()
		return
	}
	w.stopping, w.done = true, make(chan struct{})
	done := w.done
	w.mu.Unlock()
	timer := time.NewTimer(wakeShutdownGrace)
	defer timer.Stop()
	select {
	case <-done:
		return
	case <-timer.C:
	}
	w.mu.Lock()
	var abandoned []deliveryWorker
	for _, worker := range w.active {
		abandoned = append(abandoned, worker)
	}
	w.mu.Unlock()
	if len(abandoned) == 0 {
		return
	}
	slices.SortFunc(abandoned, func(a, b deliveryWorker) int { return cmp.Compare(a.ID, b.ID) })
	// The writer completed its last admitted operation before closing stopping.
	// Every late delivery result enters through query/Do, which now refuses it;
	// no worker appends the ledger itself. Abandoned results are dropped, never
	// half-written, even when a native callback finishes after Done or SIGKILL.
	slog.Warn("delivery workers exceeded shutdown grace; abandoning their results",
		"workers", abandoned, "grace", wakeShutdownGrace,
		"hint", "inspect the named delivery routes for a stuck app or command; late results cannot change the ledger")
}
