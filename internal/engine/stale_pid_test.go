// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// onePIDDead is a prober that says exactly one pid is gone. Written out rather
// than borrowed: this package's deadProber answers true for everything, so a
// test that reached for it by name would prove nothing about a dead process.
type onePIDDead struct{ dead int }

func (p onePIDDead) Alive(pid int) bool { return pid != p.dead }

// A session that moved to a new process is not a crashed agent.
//
// FOUND BY THE ARCHITECT, LOCKED OUT OF ITS OWN DECLARATION. Switching the model
// in Claude Code resumes the same session id in a NEW process. The agent's row
// still held the pid it registered with, the sweep probed that pid, found it
// gone, and marked the agent process_exited. Each call the agent made woke it
// again, which re-arms the awareness gate; check_in acknowledged the board; the
// next sweep killed it again. declare was refused on every attempt, with a hint
// saying to call check_in, which the agent had just done. A board showing an
// agent as crashed while that agent was talking to it, and a hint that loops.
func TestASessionThatMovedProcessIsNotSweptAsCrashed(t *testing.T) {
	_, sessionID := listeningSession(t) // live sidecar, pid 999001, stubbed alive
	const oldPID = 424242               // the process the agent registered from

	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, onePIDDead{dead: oldPID})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	res, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "architect", PID: oldPID, SessionID: sessionID,
		Agent: &core.AgentInfo{Harness: "Claude Code"},
	})
	if err != nil {
		t.Fatal("setup:", err)
	}
	tok, _ := res["token"].(string)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok}); err != nil {
		t.Fatal("setup: ack:", err)
	}
	e.primePeerSessions()

	_, _ = e.query(ctx, func() core.Result {
		l := e.state.Agents["architect"]
		if l.PID != oldPID {
			t.Fatalf("setup: the row holds pid %d, want the stale %d", l.PID, oldPID)
		}
		if e.prober.Alive(l.PID) {
			t.Fatal("setup: the recorded pid reads alive, so nothing below is about a moved process")
		}
		if !e.sessionMovedProcess(l) {
			t.Fatal("setup: the bound session is not in the live snapshot, so this " +
				"would be testing a crash rather than a move")
		}
		e.sweep(time.Now())
		return core.Result{}
	})

	_, _ = e.query(ctx, func() core.Result {
		l := e.state.Agents["architect"]
		if l.Status == core.StatusDormant && l.StaleReason == "process_exited" {
			t.Error("an agent whose session is alive in a new process was swept as " +
				"process_exited. Its recorded pid is stale, not dead: the harness " +
				"moved the session, and the sidecar says where")
		}
		if l.AckedSerial == 0 {
			t.Error("the sweep re-armed the awareness gate on a live agent, so its " +
				"next declare is refused with a hint to call check_in, which it just did")
		}
		return core.Result{}
	})

	// And the call that was being refused now goes through.
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSetSlot, Token: tok, Text: "still here", Activity: "implement",
	}); err != nil {
		t.Errorf("declare after the sweep: %v. This is the loop the architect was "+
			"stuck in: acknowledged, swept, refused", err)
	}
}

// The control, which is the guarantee the change must not cost: a dead pid with
// NO live session behind it is still a crash, detected at once.
func TestADeadProcessWithNoLiveSessionIsStillACrash(t *testing.T) {
	const pid = 424243
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, onePIDDead{dead: pid})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "gone", PID: pid, SessionID: "no-sidecar-for-this",
		Agent: &core.AgentInfo{Harness: "Claude Code"},
	}); err != nil {
		t.Fatal("setup:", err)
	}
	_, _ = e.query(ctx, func() core.Result {
		if e.sessionMovedProcess(e.state.Agents["gone"]) {
			t.Fatal("setup: a session with no sidecar reads as moved")
		}
		e.sweep(time.Now())
		return core.Result{}
	})
	_, _ = e.query(ctx, func() core.Result {
		if l := e.state.Agents["gone"]; l.StaleReason != "process_exited" {
			t.Errorf("a crashed agent with no live session was swept %s (%q), want "+
				"process_exited: crash detection by pid is what this change must keep",
				l.Status, l.StaleReason)
		}
		return core.Result{}
	})
}
