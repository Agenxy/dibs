// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// A starting hook records current liveness. It cannot veto a new mail event:
// the receiving harness decides how to deliver mail into an existing turn.
func TestAStartingHookRecordsContactWithoutRefusingNewMail(t *testing.T) {
	b := newContinuationBoard(t)
	if _, err := b.e.HookPoll(b.ctx, contThread, "SessionStart", "", false, true); err != nil {
		t.Fatal(err)
	}
	if _, err := b.e.query(b.ctx, func() core.Result {
		if !b.e.recentlyInTouch(b.e.state.Agents["worker"]) {
			t.Error("starting hook did not record contact")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	b.wake(t) // actual fresh mail, actual command success, no timer or later hook
}

// A late Stop from a thread the agent has LEFT must not overwrite the liveness
// of the thread it moved to. The row retains every thread it was bound to, so
// the hook resolved to the same row; recording the stop against it read the
// running thread as finished and let the next blocking message launch a second
// activation on it.
func TestALateStopFromAnOldThreadDoesNotMarkTheCurrentOneFinished(t *testing.T) {
	e := &Engine{}
	st := core.NewState("t", core.DefaultLimits())
	e.state = st
	e.seen = map[string]time.Time{}
	e.SetWakeCommands(map[string]WakeCommand{
		"codex": {Argv: []string{"echo", "{thread}"}, Cooldown: 90 * time.Second},
	})
	threadA := "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	threadB := "019ffe52-0eaf-7f60-81cc-6ab1298d76ed"
	a := bridgeAgent("mover", "Codex", threadA)
	// It has moved to thread B: B is current, A is a retained alias.
	a.SessionAliases = []string{threadA, threadB}
	a.CurrentSession = threadB
	st.Agents = map[string]*core.Agent{"mover": a}

	// B checked in just now: it is running.
	e.seen["mover"] = time.Now()
	before := e.socketLifecycle(a, time.Now())
	// A late Stop from the OLD thread A arrives.
	e.noteTurnState(a, threadA, "Stop")
	if ended, ok := e.turnEnded["mover"]; ok {
		t.Fatal("late Stop ended the current thread", ended)
	}
	if e.socketLifecycle(a, time.Now()) != before {
		t.Fatal("late Stop changed current lifecycle")
	}

	// A Stop from the CURRENT thread still ends the turn, so the fix does not
	// blind the guard to real stops.
	e.noteTurnState(a, threadB, "Stop")
	if _, ok := e.turnEnded["mover"]; !ok {
		t.Fatal("current Stop did not end the turn")
	}
	if e.socketLifecycle(a, time.Now()) != "idle" {
		t.Fatal("current Stop did not record idle")
	}
}
