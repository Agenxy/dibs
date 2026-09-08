package engine

import (
	"context"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// R23-1: a first execution that arrives through the retry path (a recency
// deferral, a boot rearm) and fails is retried once, as one from a fresh
// arrival is.
func TestADeferredFirstWakeThatFailsIsRetriedOnce(t *testing.T) {
	const thread = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	st := core.NewState("test", core.DefaultLimits())
	l := bridgeAgent("cx", "Codex", thread)
	l.Slots = map[string]core.Slot{}
	st.Agents["cx"] = l
	st.Messages[5] = &core.Message{
		Serial: 5, From: "asker", To: "cx", Type: core.MsgQuestion, State: core.MsgStatePending,
	}
	e := New(st, &memLedger{}, deadProber{})
	e.SetWakeCommands(map[string]WakeCommand{
		"codex": {Argv: []string{"/usr/bin/false", "{thread}"}, Cooldown: time.Minute},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	// The deferred first attempt, as the timer would run it: on the loop.
	if _, err := e.query(ctx, func() core.Result { e.retryWakeDecision("cx"); return core.Result{"ok": true} }); err != nil {
		t.Fatal(err)
	}
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-tick.C:
			e.wakers.mu.Lock()
			running, armed := e.wakers.running["cx"], e.wakers.deferred["cx"] != nil
			if armed {
				e.wakers.deferred["cx"].Stop()
			}
			e.wakers.mu.Unlock()
			if armed {
				return
			}
			if !running && e.wakers.attempts["cx"] > 0 {
				t.Fatal("the deferred first execution failed and no retry was armed: the " +
					"question waits for another event or a restart")
			}
		case <-deadline:
			t.Fatal("the wake never settled")
		}
	}
}
