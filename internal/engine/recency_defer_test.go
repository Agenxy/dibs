// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Recent activity cannot schedule a wake. Delivery is event-driven; only an
// actual failed delivery may arm its one bounded retry.
func TestRecentContactNeverArmsAWakeTimer(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	e.SetWakeCommands(map[string]WakeCommand{
		"codex": {Argv: []string{"/bin/echo", "{message}"}, Cooldown: time.Hour},
	})
	l := &core.Agent{
		ID: "busy", Name: "busy", Status: core.StatusActive,
		SessionID: "01a0696b-8446-7821-a992-9dc7f6a43a25",
		Agent:     &core.AgentInfo{Harness: "Codex", CWD: "/work"},
		Slots:     map[string]core.Slot{},
	}
	st.Agents["busy"] = l
	// Called Dibs a moment ago: inside the window, by a mile.
	e.seen["busy"] = time.Now()
	if !e.recentlyInTouch(l) {
		t.Fatal("setup: the agent does not read as recently in touch, so this " +
			"exercises the wrong branch entirely")
	}

	e.maybeWake(core.Event{
		Type: "message.sent", Agent: "asker", To: "busy",
		Data: map[string]any{"msg_type": core.MsgQuestion, "from": "asker"},
	})

	e.wakers.mu.Lock()
	timer := e.wakers.deferred["busy"]
	e.wakers.mu.Unlock()
	if timer != nil {
		timer.Stop()
		t.Error("recent contact armed a timer without a failed delivery")
	}
}

// Reconsideration during activity also delivers without polling.
func TestBusyReconsiderationNeverArmsAWakeTimer(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	e.SetWakeCommands(map[string]WakeCommand{
		"codex": {Argv: []string{"/bin/echo", "{message}"}, Cooldown: time.Hour},
	})
	mk := func(name, nonce string) string {
		r, _, err := st.Apply(&core.Op{
			Kind: core.OpRegister, Name: name, AgentKind: core.KindPersistent,
			Nonce: nonce, NewToken: "tok-" + name,
		}, time.Now())
		if err != nil {
			t.Fatal("setup:", err)
		}
		tok, _ := r["token"].(string)
		return tok
	}
	sender := mk("asker", "n-a")
	mk("busy", "n-b")
	l := st.Agents["busy"]
	l.Agent = &core.AgentInfo{Harness: "Codex", CWD: "/work"}
	l.SessionID = "01a0696b-8446-7821-a992-9dc7f6a43a25"

	if _, _, err := st.Apply(&core.Op{
		Kind: core.OpSendMessage, Token: sender, To: "busy",
		MsgType: core.MsgQuestion, Body: "blocked on you",
	}, time.Now()); err != nil {
		t.Fatal("setup:", err)
	}
	if !e.hasBlockingMail("busy") {
		t.Fatal("setup: nothing is blocking, so there is nothing to keep asking about")
	}
	e.seen["busy"] = time.Now() // still working

	e.retryWakeDecision("busy")

	e.wakers.mu.Lock()
	timer := e.wakers.deferred["busy"]
	e.wakers.mu.Unlock()
	if timer != nil {
		timer.Stop()
		t.Error("busy reconsideration armed a timer without a failed delivery")
	}
}

// Drive register, check_in and send through the writer. No Stop hook, later
// message or reconnect is allowed to rescue the original question.
func TestQuestionThirtySecondsAfterCheckInDeliversWithoutAnotherEvent(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	out := filepath.Join(t.TempDir(), "delivery")
	e.SetWakeCommands(map[string]WakeCommand{
		"codex": {Argv: []string{os.Args[0], "-test.run=^TestRecentContactCommandHelper$", "--", out}, Cooldown: 90 * time.Second},
	})
	ctx, cancel := context.WithCancel(context.Background())
	joined := make(chan struct{})
	go func() { e.Run(ctx); close(joined) }()
	// The child writes its receipt before it exits. Join the wake while its
	// writer is still available, before another test restores the app fixture.
	t.Cleanup(func() { waitWakeDone(t, e, "worker"); cancel(); <-joined })
	register := func(name string, agent *core.AgentInfo, session string) string {
		r, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: name, Nonce: "recent-" + name, AgentKind: core.KindPersistent, Agent: agent, SessionID: session})
		if err != nil {
			t.Fatal("setup register:", err)
		}
		tok, _ := r["token"].(string)
		if tok == "" {
			t.Fatal("setup: missing credential", r)
		}
		return tok
	}
	recipient := register("worker", &core.AgentInfo{Harness: "Codex"}, "01a0696b-8446-7821-a992-9dc7f6a43a25")
	sender := register("asker", nil, "")
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: recipient}); err != nil {
		t.Fatal("setup check_in:", err)
	}
	<-time.After(30 * time.Second)
	if _, err := e.query(ctx, func() core.Result {
		if !e.recentlyInTouch(st.Agents["worker"]) {
			t.Error("setup: actual check_in did not establish recent contact")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: sender, To: "worker", MsgType: core.MsgQuestion, Body: "original question"}); err != nil {
		t.Fatal("setup send:", err)
	}
	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if b, err := os.ReadFile(out); err == nil && string(b) == "delivered" {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline:
			t.Fatal("question arriving 30s after check_in was stranded without another event")
		}
	}
}

func TestRecentContactCommandHelper(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "--" && i+1 < len(os.Args) {
			if err := os.WriteFile(os.Args[i+1], []byte("delivered"), 0o600); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
}
