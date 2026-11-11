// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/testcodexipc"
)

const acceptedContactTestBound = 30 * time.Minute

type acceptedContactFixture struct {
	e              *Engine
	ctx            context.Context
	ipc            *testcodexipc.Server
	worker, sender string
}

func newAcceptedContactFixture(t *testing.T) *acceptedContactFixture {
	t.Helper()
	s := testcodexipc.Start(t, "idle", nil)
	e, ctx, worker, sender := nativeAppEngine(t)
	e.SetHumanNotifier(noContactDesktop{})
	return &acceptedContactFixture{e, ctx, s, worker, sender}
}

func (f *acceptedContactFixture) send(t *testing.T, kind string, n int, priority ...string) (uint64, time.Time) {
	t.Helper()
	op := &core.Op{Kind: core.OpSendMessage, Token: f.sender, To: "worker", MsgType: kind, Body: "private accepted-contact marker"}
	if len(priority) != 0 {
		op.RequestPriority = priority[0]
	}
	r, err := f.e.Do(f.ctx, op)
	if err != nil {
		t.Fatal("setup send:", err)
	}
	id, ok := r["msg_serial"].(uint64)
	if !ok {
		t.Fatal("setup send returned no serial")
	}
	awaitNative(t, f.ipc, n)
	waitWakeDone(t, f.e, "worker")
	r, err = f.e.query(f.ctx, func() core.Result { return f.e.queueWakeView(f.e.state.Agents["worker"], time.Now()) })
	if err != nil || r["native_delivery"] != "accepted" {
		t.Fatalf("setup native receipt: %v %v", r, err)
	}
	at, ok := r["observed_at"].(time.Time)
	if !ok || at.IsZero() {
		t.Fatal("setup receipt clock missing")
	}
	return id, at
}

func (f *acceptedContactFixture) sweep(t *testing.T, at time.Time, ids ...uint64) core.Result {
	t.Helper()
	r, err := f.e.query(f.ctx, func() core.Result {
		f.e.sweep(at)
		v := core.Result{"contacts": len(f.e.state.Contacts)}
		for _, id := range ids {
			if m := f.e.state.Messages[id]; m != nil {
				v["contact"] = m.ContactEscalatedAt
				v["pending"] = m.State == core.MsgStatePending
				v["consumed"] = m.Consumed
				if c := f.e.state.Contacts[m.ContactEscalatedAt]; c != nil {
					n, _ := f.e.contactNotice(c)
					v["hint"] = n.Contact.OpenHint
				}
			}
		}
		return v
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAcceptedWakeWithoutContactEscalatesAtBound(t *testing.T) {
	for _, kind := range []string{core.MsgQuestion, core.MsgRequest, core.MsgHandoff} {
		t.Run(kind, func(t *testing.T) {
			f := newAcceptedContactFixture(t)
			id, at := f.send(t, kind, 1)
			if before := f.sweep(t, at.Add(acceptedContactTestBound-time.Second), id); before["contacts"] != 0 {
				t.Fatalf("early contact: %v", before)
			}
			after := f.sweep(t, at.Add(acceptedContactTestBound+time.Second), id)
			if after["contacts"] != 1 || after["contact"] == uint64(0) || after["pending"] != true || after["consumed"] != false {
				t.Fatalf("accepted wake with no authenticated model call never escalated: %v", after)
			}
			hint, _ := after["hint"].(string)
			if !strings.Contains(hint, "delivery unconfirmed") || strings.Contains(hint, "private accepted-contact marker") ||
				strings.Contains(hint, "agent dead") || strings.Contains(hint, "process gone") {
				t.Fatalf("contact alert is not honest metadata: %q", hint)
			}
			if again := f.sweep(t, at.Add(2*acceptedContactTestBound), id); again["contacts"] != 1 {
				t.Fatalf("repeat sweep duplicated contact: %v", again)
			}
		})
	}
}

func TestLaterAcceptanceDoesNotResetOldestUnreadClock(t *testing.T) {
	f := newAcceptedContactFixture(t)
	first, at := f.send(t, core.MsgQuestion, 1)
	second, _ := f.send(t, core.MsgQuestion, 2)
	if after := f.sweep(t, at.Add(acceptedContactTestBound), first); after["contact"] == uint64(0) {
		t.Fatalf("later acceptance reset the original unread interval: %v", after)
	}
	r, err := f.e.query(f.ctx, func() core.Result { return core.Result{"contact": f.e.state.Messages[second].ContactEscalatedAt} })
	if err != nil || r["contact"] != uint64(0) {
		t.Fatalf("fresh cohort inherited older age: %v %v", r, err)
	}
}

func TestAuthenticatedContactCancelsAcceptedWakeEscalation(t *testing.T) {
	f := newAcceptedContactFixture(t)
	id, at := f.send(t, core.MsgQuestion, 1)
	if _, err := f.e.SpaceRead(f.ctx, f.worker, "does-not-exist", 1); err == nil {
		t.Fatal("setup: missing space existed")
	}
	if got := f.sweep(t, at.Add(2*acceptedContactTestBound), id); got["contacts"] != 0 {
		t.Fatalf("real token-authenticated contact still escalated: %v", got)
	}
}

func TestBackgroundObservationCannotConfirmAcceptedWake(t *testing.T) {
	f := newAcceptedContactFixture(t)
	id, at := f.send(t, core.MsgQuestion, 1)
	if _, err := f.e.EventsSince(f.ctx, f.worker, 0, false); err != nil {
		t.Fatal("setup observer:", err)
	}
	if got := f.sweep(t, at.Add(acceptedContactTestBound+time.Second), id); got["contacts"] != 1 {
		t.Fatalf("background observer hid missing model contact: %v", got)
	}
}

func TestAcceptedNotifyDoesNotEnterMissingContactEscalation(t *testing.T) {
	for _, priority := range []string{"normal", "high", "urgent"} {
		t.Run(priority, func(t *testing.T) {
			f := newAcceptedContactFixture(t)
			id, at := f.send(t, core.MsgNotify, 1, priority)
			if got := f.sweep(t, at.Add(2*acceptedContactTestBound), id); got["contacts"] != 0 {
				t.Fatalf("notify incorrectly escalated: %v", got)
			}
		})
	}
}

func TestFreshMailAfterModelContactGetsItsOwnAcceptedInterval(t *testing.T) {
	f := newAcceptedContactFixture(t)
	first, at := f.send(t, core.MsgQuestion, 1)
	if _, err := f.e.SpaceRead(f.ctx, f.worker, "does-not-exist", 1); err == nil {
		t.Fatal("setup missing space existed")
	}
	second, later := f.send(t, core.MsgQuestion, 2)
	if got := f.sweep(t, at.Add(acceptedContactTestBound), second); got["contacts"] != 0 {
		t.Fatalf("new acceptance inherited the pre-contact interval: %v", got)
	}
	if got := f.sweep(t, later.Add(acceptedContactTestBound+time.Second), second); got["contact"] == uint64(0) {
		t.Fatalf("fresh mail after contact never escalated: %v", got)
	}
	r, err := f.e.query(f.ctx, func() core.Result {
		return core.Result{"contact": f.e.state.Messages[first].ContactEscalatedAt}
	})
	if err != nil || r["contact"] != uint64(0) {
		t.Fatalf("cleared pre-contact cohort was revived: %v %v", r, err)
	}
}

func TestAcceptedContactDoesNotSurviveRouteChange(t *testing.T) {
	f := newAcceptedContactFixture(t)
	id, at := f.send(t, core.MsgQuestion, 1)
	f.e.SetWakeCommands(nil)
	if got := f.sweep(t, at.Add(2*acceptedContactTestBound), id); got["contacts"] != 0 {
		t.Fatalf("old accepted endpoint escalated after route replacement: %v", got)
	}
}

func TestModelContactRacingNativeResultCancelsEscalation(t *testing.T) {
	ready := make(chan struct{})
	called := make(chan error, 1)
	var f *acceptedContactFixture
	var armed atomic.Bool
	s := testcodexipc.Start(t, "idle", func() {
		if !armed.Load() {
			return // registration's read-only ownership probe is setup
		}
		<-ready
		_, err := f.e.SpaceRead(f.ctx, f.worker, "does-not-exist", 1)
		called <- err
	})
	e, ctx, worker, sender := nativeAppEngine(t)
	e.SetHumanNotifier(noContactDesktop{})
	f = &acceptedContactFixture{e, ctx, s, worker, sender}
	armed.Store(true)
	close(ready)
	id, at := f.send(t, core.MsgQuestion, 1)
	select {
	case err := <-called:
		if err == nil {
			t.Fatal("setup missing space existed")
		}
	case <-time.After(time.Second):
		t.Fatal("setup native discovery never reached real model contact")
	}
	if got := f.sweep(t, at.Add(2*acceptedContactTestBound), id); got["contacts"] != 0 {
		t.Fatalf("late acceptance callback erased real authenticated contact: %v", got)
	}
}

func TestRecipientReadCancelsAcceptedContact(t *testing.T) {
	f := newAcceptedContactFixture(t)
	id, at := f.send(t, core.MsgQuestion, 1)
	if _, err := f.e.ReadMail(f.ctx, f.worker, id, false); err != nil {
		t.Fatal("setup recipient awareness:", err)
	}
	if got := f.sweep(t, at.Add(2*acceptedContactTestBound), id); got["contacts"] != 0 || got["pending"] != false {
		t.Fatalf("recipient awareness did not cancel accepted gap: %v", got)
	}
}

func TestRestartDoesNotInventAcceptedContactEvidence(t *testing.T) {
	f := newAcceptedContactFixture(t)
	id, at := f.send(t, core.MsgQuestion, 1)
	var folded *core.State
	result, err := f.e.query(f.ctx, func() core.Result {
		folded = core.NewState("restarted", core.DefaultLimits())
		for _, op := range f.e.led.(*memLedger).ops {
			if _, _, applyErr := folded.Apply(op, time.Now()); applyErr != nil {
				return core.Result{"error": applyErr}
			}
		}
		return nil
	})
	if err != nil || result["error"] != nil || folded == nil || folded.Messages[id] == nil {
		t.Fatalf("setup ledger fold: %v %v", result, err)
	}
	restarted := New(folded, &memLedger{}, deadProber{})
	// No route is installed on this newly constructed engine: replay itself
	// must not manufacture an accepted receipt. Use the actual sweep directly
	// on its writer, with normal ownership and shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	joined := make(chan struct{})
	go func() { restarted.Run(ctx); close(joined) }()
	t.Cleanup(func() { cancel(); <-joined })
	ff := &acceptedContactFixture{e: restarted, ctx: ctx}
	if got := ff.sweep(t, at.Add(2*acceptedContactTestBound), id); got["contacts"] != 0 || got["pending"] != true {
		t.Fatalf("restart invented delivery evidence or lost mail: %v", got)
	}
}
