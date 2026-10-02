package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Enter through each observation API, not a hand-stamped recency field. A
// parked long-poll is machinery waiting between turns, not a model taking one.
func TestEventObserversDoNotSuppressAnIdleSessionsSocketWake(t *testing.T) {
	for _, method := range []string{"await_events", "events_since", "recent_events"} {
		t.Run(method, func(t *testing.T) {
			sock, sid := listeningSession(t)
			e := New(core.NewState("polling", core.DefaultLimits()), &memLedger{}, deadProber{})
			// A configured command supplies the recency window. The listening
			// socket still takes precedence over it in the real wake route.
			e.SetWakeCommands(map[string]WakeCommand{"claude code": {Argv: []string{"/usr/bin/true", "{thread}"}, Cooldown: time.Hour}})
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			stopWakeTimersOnCleanup(t, e)
			go e.Run(ctx)
			register := func(id, session string) string {
				r, err := e.Do(ctx, &core.Op{
					Kind: core.OpRegister, Name: id,
					Nonce: "polling-fixture-0123456789abcdef-" + id, SessionID: session,
					Agent: &core.AgentInfo{Harness: "Claude Code", CWD: "/w"},
				})
				if err != nil {
					t.Fatal("register setup:", err)
				}
				return r["token"].(string)
			}
			worker := register("worker", sid)
			sender := register("sender", "")
			if _, err := e.HookPoll(ctx, sid, "Stop", "", false, false); err != nil {
				t.Fatal("stop setup:", err)
			}
			at := uint64(0)
			if _, err := e.query(ctx, func() core.Result {
				if e.recentlyInTouch(e.state.Agents["worker"]) {
					t.Error("setup: Stop did not end the turn")
				}
				at = e.state.Serial
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if t.Failed() {
				return
			}
			poll := make(chan error, 1)
			switch method {
			case "await_events":
				go func() { _, err := e.AwaitEvents(ctx, worker, at, time.Second, false); poll <- err }()
				parked := false
				for range 100 {
					if _, err := e.query(ctx, func() core.Result { parked = len(e.watch) > 0; return nil }); err != nil {
						t.Fatal(err)
					}
					if parked {
						break
					}
					<-time.After(time.Millisecond)
				}
				if !parked {
					t.Fatal("setup: await_events never parked")
				}
			case "events_since":
				if _, err := e.EventsSince(ctx, worker, at, false); err != nil {
					t.Fatal(err)
				}
			case "recent_events":
				if _, err := e.RecentEvents(ctx, worker, 10); err != nil {
					t.Fatal(err)
				}
			}
			wire := make(chan string, 1)
			go func() { wire <- readAll(t, sock) }()
			if _, err := e.Do(ctx, &core.Op{
				Kind: core.OpSendMessage, Token: sender,
				To: "worker", MsgType: core.MsgQuestion, Body: "polling-wake-marker",
			}); err != nil {
				t.Fatal("send setup:", err)
			}
			select {
			case got := <-wire:
				if !strings.Contains(got, "polling-wake-marker") {
					t.Fatalf("socket did not carry the question: %s", got)
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatal("an event observer after Stop suppressed the idle session's socket wake")
			}
			if method == "await_events" {
				select {
				case err := <-poll:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(time.Second):
					t.Fatal("parked observer did not receive the event")
				}
			}
		})
	}
}
