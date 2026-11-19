// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

// Preserve the old late-reopen incarnation guard at the remaining asynchronous
// boundary. Both attempts enter through real send; the newer probe stays held.
func TestLateClaudeProbeCannotTouchAReplacementRecipient(t *testing.T) {
	f := newReopenContactFixture(t)
	oldRelease, newRelease := make(chan struct{}), make(chan struct{})
	entered := make(chan int32, 4)
	var count atomic.Int32
	var releaseOld, releaseNew sync.Once
	previous := claudeRunning
	claudeRunning = func(string) bool {
		n := count.Add(1)
		entered <- n
		if n == 1 {
			<-oldRelease
		} else {
			<-newRelease
		}
		return false
	}
	t.Cleanup(func() {
		f.cancel()
		releaseOld.Do(func() { close(oldRelease) })
		releaseNew.Do(func() { close(newRelease) })
		<-f.joined
		claudeRunning = previous
	})
	waitProbe := func(want int32) {
		t.Helper()
		select {
		case n := <-entered:
			if n != want {
				t.Fatalf("setup: probe=%d, want %d", n, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("setup: real send did not reach the process probe")
		}
	}
	f.do(t, &core.Op{Kind: core.OpSendMessage, Token: f.sender, To: "lead", MsgType: core.MsgRequest, Body: "old request"})
	waitProbe(1)
	removed, err := f.e.query(f.ctx, func() core.Result {
		old := f.e.state.Agents["lead"]
		archived := old.DormantSince.Add(f.e.state.Limits.DormancyMax + time.Second)
		for _, at := range []time.Time{archived, archived.Add(f.e.state.Limits.ArchiveRetention + time.Second)} {
			op := &core.Op{Kind: core.OpSweep, PurgeMail: true, V7Semantics: true, KeepOwed: true, KeepArchivedNonce: true}
			if _, err := f.e.applyAndLedger(op, at); err != nil {
				return core.Result{"error": err}
			}
		}
		return core.Result{"removed": f.e.state.Agents["lead"] == nil}
	})
	if err != nil || removed["removed"] != true {
		t.Fatalf("setup: real lifecycle did not release the old identity: %v %v", removed, err)
	}
	replacement := f.do(t, &core.Op{
		Kind: core.OpRegister, Name: "lead", Nonce: "probe-contact-replacement",
		AgentKind: core.KindPersistent, SessionID: "6336088d-1111-4222-8333-444455556666",
		Agent: &core.AgentInfo{Harness: "Claude Code", Surface: harnessenv.ClaudeDesktop, CWD: t.TempDir()},
	})
	if replacement["agent_id"] != "lead" {
		t.Fatalf("setup: replacement did not reuse the id: %v", replacement)
	}
	f.do(t, &core.Op{Kind: core.OpSweep, DeadAgents: []string{"lead"}})
	sent := f.do(t, &core.Op{
		Kind: core.OpSendMessage, Token: f.sender, To: "lead",
		MsgType: core.MsgRequest, Body: "replacement request",
	})
	serial := sent["msg_serial"].(uint64)
	waitProbe(2)
	releaseOld.Do(func() { close(oldRelease) })
	// Keep the original one-second observation while the replacement's probe
	// is still blocked. Only the old callback can affect its state here.
	<-time.After(time.Second)
	state, err := f.e.query(f.ctx, func() core.Result {
		m := f.e.state.Messages[serial]
		return core.Result{
			"contact": m.ContactEscalatedAt, "contacts": len(f.e.state.Contacts),
			"state": m.State, "created": f.e.state.Agents["lead"].CreatedSerial, "alerts": len(f.e.appBindings),
		}
	})
	if err != nil || state["created"].(uint64) <= f.created || state["contact"] != uint64(0) ||
		state["contacts"] != 0 || state["state"] != core.MsgStatePending || state["alerts"] != 0 {
		t.Fatalf("old probe touched replacement mail or its diagnosis: %v %v", state, err)
	}
	f.e.wakeWorkers.mu.Lock()
	active := len(f.e.wakeWorkers.active)
	f.e.wakeWorkers.mu.Unlock()
	if count.Load() != 2 || active != 1 {
		t.Fatalf("setup: old callback did not finish while the replacement stayed held: probes=%d workers=%d", count.Load(), active)
	}
	select {
	case argv := <-f.opened:
		t.Fatalf("lookup recovery contacted an app: %q", argv)
	default:
	}
}
