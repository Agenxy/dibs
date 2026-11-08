// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// startProber knows which pids exist and when each one started. Mutated only
// inside e.query, so the loop is the only goroutine that touches it.
type startProber struct{ started map[int]int64 }

func (p *startProber) Alive(pid int) bool { _, ok := p.started[pid]; return ok }

func (p *startProber) StartMillis(pid int) (int64, bool) {
	ms, ok := p.started[pid]
	return ms, ok
}

// pidOnlyProber can say a pid exists and nothing about when it started, which
// is every Prober before StartClock and every platform without one.
type pidOnlyProber struct{}

func (pidOnlyProber) Alive(int) bool { return true }

func registerQuiet(t *testing.T, e *Engine, ctx context.Context, name string, pid int) {
	t.Helper()
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: name, PID: pid, SessionID: "no-sidecar-" + name,
		Agent: &core.AgentInfo{Harness: "Codex"},
	}); err != nil {
		t.Fatal("setup:", err)
	}
}

// A worker whose own process is running is not dormant for being quiet.
//
// Agents complained for weeks that colleagues who were plainly working read
// as dormant, and on 2026-11-08 13 of the board's 42 dormant rows had a
// process the daemon had just confirmed alive, the architect among them while
// it was making a call. Building, testing and waiting on a person are all
// time not spent calling Dibs. The registration goes through e.Do, so the
// start time is stamped by the same code production runs, not set by hand.
func TestAQuietAgentWhoseProcessIsRunningStaysActive(t *testing.T) {
	const pid = 515151
	prober := &startProber{started: map[int]int64{pid: 1_700_000_000_123}}
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, prober)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	registerQuiet(t, e, ctx, "worker", pid)

	later := time.Now().Add(st.Limits.IdleTTL + time.Hour)
	_, _ = e.query(ctx, func() core.Result {
		if l := e.state.Agents["worker"]; l.ProcStart != prober.started[pid] {
			t.Errorf("setup: register recorded proc_start %d, want the process's %d",
				l.ProcStart, prober.started[pid])
		}
		e.sweep(later)
		return core.Result{}
	})
	_, _ = e.query(ctx, func() core.Result {
		if l := e.state.Agents["worker"]; l.Status != core.StatusActive {
			t.Errorf("an agent whose own process is running was swept %s (%q) for an "+
				"hour of not calling Dibs", l.Status, l.StaleReason)
		}
		return core.Result{}
	})

	// The pid is handed to another program: same number, new start time. That
	// is the case the clock was kept for, and it must still catch it.
	_, _ = e.query(ctx, func() core.Result {
		prober.started[pid]++
		e.sweep(later.Add(time.Minute))
		return core.Result{}
	})
	_, _ = e.query(ctx, func() core.Result {
		if l := e.state.Agents["worker"]; l.Status == core.StatusActive {
			t.Error("a recycled pid (alive, different start time) kept a silent agent " +
				"active: kill(pid, 0) alone cannot tell the agent's process from a stranger's")
		}
		return core.Result{}
	})
}

// The control: with no start time to compare, a live pid proves nothing about
// whose process it is, so silence is still judged by the clock.
func TestAQuietAgentWithNoStartTimeIsStillJudgedByTheClock(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, pidOnlyProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	registerQuiet(t, e, ctx, "unknown", 525252)

	_, _ = e.query(ctx, func() core.Result {
		if l := e.state.Agents["unknown"]; l.ProcStart != 0 {
			t.Errorf("setup: proc_start %d recorded by a prober with no start clock", l.ProcStart)
		}
		e.sweep(time.Now().Add(st.Limits.IdleTTL + time.Hour))
		return core.Result{}
	})
	_, _ = e.query(ctx, func() core.Result {
		if l := e.state.Agents["unknown"]; l.Status == core.StatusActive {
			t.Error("a silent agent with no start time stayed active: without one a live " +
				"pid could be anybody's")
		}
		return core.Result{}
	})
}
