package engine

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// HumanAgent must not read core's maps off the writer loop.
//
// THE CRASH THIS CATCHES. HumanAgent confirmed its cached token by calling
// state.AgentByToken directly, from whichever goroutine wanted the human: an
// HTTP handler, a notification path, the board. That method ITERATES
// State.Agents, and the writer loop writes to the same map on every
// registration. e.human.mu protects the cached fields beside it and nothing
// inside core, so this was an unguarded concurrent map iteration and write,
// which Go turns into `fatal error` and takes the daemon down with.
//
// It is worth saying why the suite did not catch it. Nothing else calls
// HumanAgent while registrations are in flight, so `go test -race` stayed green
// against genuinely racy code for as long as it existed. RepairHumanProcess and
// HumanTouch were corrected for this exact class by an earlier review and this
// one was missed both times, which is the argument for a probe rather than a
// re-reading.
//
// Run with -race, where it is a race detector report; without it, the map
// panics on its own often enough to matter.
func TestHumanAgentDoesNotReadStateOffTheLoop(t *testing.T) {
	// Built here rather than with runningEngine, because the ceiling has to be
	// raised before the engine takes the state: 400 registrations is well past
	// the default and a refusal would end the writer traffic this depends on.
	st := core.NewState("test", core.DefaultLimits())
	st.Limits.MaxAgents = 4000
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	if _, _, err := e.HumanAgent(ctx); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var wg sync.WaitGroup
	for reader := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 250 {
				if _, _, err := e.HumanAgent(ctx); err != nil {
					t.Errorf("reader %d: %v", reader, err)
					return
				}
			}
		}()
	}
	for i := range 400 {
		if _, err := e.Do(ctx, &core.Op{
			Kind: core.OpRegister, Name: fmt.Sprintf("writer-%d", i),
			NewToken: fmt.Sprintf("tok-%d", i),
		}); err != nil {
			t.Fatalf("register %d: %v", i, err)
		}
	}
	wg.Wait()
}

// HumanAgent must not freeze the daemon by holding its mutex across the loop.
//
// THE DEADLOCK THIS CATCHES. HumanAgent took e.human.mu with a defer and held
// it while calling e.query and e.Do. The writer loop is one goroutine, and the
// requests it serves take that same mutex: board rendering does, on every
// call. So HumanAgent could lock the mutex, the writer could pick up a board
// request, that request would block on the mutex, and HumanAgent would block
// waiting for the writer now stuck behind it. Nothing else can be served after
// that: the single receiver of e.ops is gone and every agent hangs.
//
// The race probe above cannot see this, which is why it is a separate test: its
// competing traffic is registrations, and registrations never touch human.mu.
// Here the competing traffic is Board, which does.
//
// Written as a deadline rather than an assertion, because the failure mode is
// not a wrong answer, it is no answer ever.
func TestHumanAgentDoesNotFreezeTheWriterLoop(t *testing.T) {
	e, ctx, cancel := runningEngine(t)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range 100 {
					if _, _, err := e.HumanAgent(ctx); err != nil {
						return
					}
				}
			}()
		}
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range 100 {
					if _, err := e.Board(ctx); err != nil {
						return
					}
				}
			}()
		}
		wg.Wait()
	}()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("HumanAgent and Board deadlocked: the mutex was held across a trip " +
			"through the writer loop, and the loop serves requests that take it. " +
			"Nothing on this board can be served again")
	}
}
