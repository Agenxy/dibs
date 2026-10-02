package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestSocketAndStopSharePresentationOnlyAfterTurnEvidence(t *testing.T) {
	for _, order := range []string{"accepted socket then Stop", "held socket then Stop", "Stop then socket"} {
		t.Run(order, func(t *testing.T) {
			sock, sid := listeningSession(t)
			st := core.NewState("dedupe", core.DefaultLimits())
			for _, id := range []string{"sender", "worker"} {
				session := ""
				if id == "worker" {
					session = sid
				}
				if _, _, err := st.Apply(&core.Op{
					Kind: core.OpRegister, Name: id, NewToken: "tok-" + id,
					SessionID: session, Agent: &core.AgentInfo{Harness: "Claude Code", CWD: "/w"},
				}, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			r, _, err := st.Apply(&core.Op{
				Kind: core.OpSendMessage, Token: "tok-sender", To: "worker",
				MsgType: core.MsgQuestion, Body: "dedupe-marker",
			}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			serial := r["msg_serial"].(uint64)
			e := New(st, &memLedger{}, deadProber{})
			e.primePeerSessions()
			plan, ok := e.wakeFor(st.Agents["worker"], core.MsgQuestion, questionFor("worker"))
			if !ok || !strings.Contains(plan.notice, "dedupe-marker") {
				t.Fatal("setup: no socket plan with the message")
			}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			stopWakeTimersOnCleanup(t, e)
			go e.Run(ctx)
			stop := func() core.Result {
				got, err := e.HookPoll(ctx, sid, "Stop", "", false, false)
				if err != nil {
					t.Fatal(err)
				}
				return got
			}
			if order == "Stop then socket" {
				if stop()["reason"] == nil {
					t.Fatal("setup: Stop did not present the question")
				}
			}
			wire := make(chan string, 1)
			go func() { wire <- readAll(t, sock) }()
			if !e.runWakeAndReport(plan, "worker") {
				t.Fatal("socket delivery failed")
			}
			if order == "Stop then socket" {
				select {
				case got := <-wire:
					t.Fatalf("Stop already presented #%d; socket duplicated it: %s", serial, got)
				case <-time.After(150 * time.Millisecond):
				}
			} else {
				select {
				case got := <-wire:
					if !strings.Contains(got, "dedupe-marker") {
						t.Fatal("setup: wire missing question")
					}
				case <-time.After(time.Second):
					t.Fatal("setup: no socket write")
				}
				if order == "accepted socket then Stop" {
					// Real tool activity, not a timestamp set by the test. Reading
					// the board is evidence of a new turn but does not read mail.
					if _, err := e.SpaceRead(ctx, "tok-worker", "missing-space", 1); err == nil {
						t.Fatal("setup: nonexistent space unexpectedly existed")
					}
				}
				got := stop()
				if order == "accepted socket then Stop" && got["reason"] != nil {
					t.Fatalf("socket plus subsequent turn evidence already presented #%d; Stop duplicated it: %v", serial, got)
				}
				if order == "held socket then Stop" && got["reason"] == nil {
					t.Fatal("a successful write with no turn evidence suppressed the held socket's Stop fallback")
				}
			}
			if _, err := e.query(ctx, func() core.Result {
				m := e.state.Messages[serial]
				if m == nil || m.State != core.MsgStatePending || m.Consumed {
					t.Error("presentation changed the unread mailbox")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
