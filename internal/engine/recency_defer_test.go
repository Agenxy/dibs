// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Recent activity is a refusal, never a schedule. Only a failed delivery may
// arm a bounded retry; another mail or reconnect event reconsiders the route.
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

// Reconsideration during activity also refuses without polling.
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
