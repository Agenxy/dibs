package engine

import (
	"testing"
	"time"
)

// The lock the fault reporter holds across the writer loop must not be the lock
// the writer loop itself takes.
//
// THE FREEZE THIS CATCHES. dibsAgent holds a mutex across e.Do, which sends on
// an UNBUFFERED channel and waits for the loop. flushFaults runs ON that loop,
// from the one-second tick, and took the same mutex. So: the reporter takes the
// lock, the tick lands, the loop blocks acquiring it, the reporter waits for
// the loop that is now stuck behind it, and the only receiver of e.ops is gone.
// Every agent on the board stops. Found by a pre-release review.
//
// The cycle was mine: flushFaults went on the loop in the fix for faults that
// were never retried. Third instance of this shape from me.
//
// WRITTEN AS A STRUCTURAL TEST, and the reason matters. The first two versions
// of this were scheduled probes: report a lot of faults, keep the loop busy,
// hope a tick lands inside the window. Both passed against the deadlocking
// code, because dibsAgent caches its identity, so there is exactly ONE window
// in the life of an engine and it is one op long. A probe that has to win a
// race is a probe that reports "fixed" on most runs of broken code.
//
// So this holds the reporter's lock deliberately and asks whether the loop can
// still be reached. That is the actual invariant, and it does not depend on
// timing at all.
func TestTheFaultReportersLockIsNotTheLoopsLock(t *testing.T) {
	e, ctx, cancel := runningEngine(t)
	defer cancel()

	// Hold the bookkeeping lock the writer loop takes on every tick.
	e.faults.mu.Lock()
	defer e.faults.mu.Unlock()

	// The reporter must still be able to mint its identity, which goes through
	// the loop. If it needs the lock above, this never returns.
	done := make(chan error, 1)
	go func() {
		_, _, err := e.dibsAgent(ctx)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("dibsAgent: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the fault reporter blocked on the same mutex the writer loop takes " +
			"on every tick. When the tick wins that race the loop stops, the " +
			"reporter waits for the loop, and no agent on this board is served again")
	}

	// And the loop is demonstrably still serving.
	if _, err := e.Board(ctx); err != nil {
		t.Errorf("the board stopped answering while the fault lock was held: %v", err)
	}
}
