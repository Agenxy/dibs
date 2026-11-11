// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"log/slog"
	"time"
)

// Run drives the loop until ctx is done. Call in exactly one goroutine.
func (e *Engine) Run(ctx context.Context) {
	defer close(e.done)
	// A committed advice send still has to save its derived suppression
	// memory. Closing Done first would let the daemon exit in that window and
	// announce the same unchanged advice again on its next boot. The writer
	// alone starts these workers, so none can be added after this loop exits.
	defer e.advisories.workers.Wait()
	reconcileContext, eCancel := context.WithCancel(ctx)
	e.blobReconcileContext = reconcileContext
	e.wakeContext = reconcileContext
	e.startHumanCleanup(reconcileContext)
	defer func() {
		close(e.stopping) // reject new queries before joining workers that may submit one
		eCancel()         // also unblock completion receipts on a fail-stop writer panic
		e.waitWakeWorkers()
		e.finishBlobReconciles()
	}()
	e.boot(time.Now())
	go e.watchAppRestarts(ctx)
	e.requestHumanCleanup(e.humanCleanupAt(time.Now()))
	e.reconcileBlobs() // startup reconcile: drop crash orphans (A4.1)
	// Prime the cache before serving.
	// The wake gate reads a cache and refuses when it is cold, because finding
	// the sockets a harness publishes costs a directory read and a bounded `ps`
	// per candidate, which must never happen on the writer loop. Priming in a
	// goroutine left a window where an event could arrive first, be refused
	// before any cooldown or deferral state existed, and never be reconsidered:
	// the first socket wake of a daemon's life, lost outright. Found by the
	// pre-release review.
	//
	// Every startup probe is bounded.
	e.primePeerSessions()
	if n := e.rearmDeferredWakes(); n > 0 {
		slog.Info("wake: reconsidered blocking mail at the daemon reconnect event", "agents", n)
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	reconcileTick := time.NewTicker(30 * time.Second)
	defer reconcileTick.Stop()
	defer e.stopWakeBursts()
	for {
		select {
		case <-ctx.Done():
			return
		case <-reconcileTick.C:
			e.reconcileBlobs()       // delete evicted/orphan blob files off-thread
			go e.primePeerSessions() // sessions come and go; keep the cache warm
		case req := <-e.ops:
			e.serveRequest(req)
		case now := <-tick.C:
			e.sweep(now)
			e.expireWaiters(now)
			e.pruneSocketState()
		case s := <-e.subs:
			e.streams[s.ch] = s.lost
			// Catch-up replay: the `default` drops
			// events once the subscriber's buffer is full rather than blocking.
			//
			// Blocking here would stall the SINGLE WRITER on a slow reader,
			// one wedged browser tab would freeze the whole fleet, so dropping
			// is the only correct policy. It is safe because the SSE stream
			// carries a full board SNAPSHOT in every frame: a subscriber that
			// misses replayed events still renders correct state, and the only
			// loss is rows in the on-screen activity list. The ledger, which is
			// the actual record, is untouched.
			//
			// Written down because a bare `default` reads like an oversight, and
			// the next person to see it should not "fix" it into a block.
			for _, ev := range e.eventsSince(s.since, "", true) {
				select {
				case s.ch <- ev:
				default:
					s.lost.Store(true)
				}
			}
		case ch := <-e.unsubs:
			delete(e.streams, ch)
			close(ch)
		}
	}
}

// Done bounds admitted off-loop observations by the writer's lifetime rather
// than an individual HTTP request. It is available before Run starts.
func (e *Engine) Done() <-chan struct{} { return e.done }

// Decision fixtures have no running writer; real delivery workers always use
// Run's context so stopping cancels their transports before they are joined.
func (e *Engine) deliveryContext() context.Context {
	if e.wakeContext != nil {
		return e.wakeContext
	}
	return context.Background()
}
