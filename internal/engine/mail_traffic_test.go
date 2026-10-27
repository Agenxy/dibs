// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Drive the real archive/GC/register fold before crossing the authenticated
// reader door. An ID spelling is reusable; its prior holder's counts are not.
func TestTrafficSelfReadFencesReusedIDAndDoesNotConfirmContact(t *testing.T) {
	lim := core.DefaultLimits()
	lim.ArchiveRetention = 0
	lim.StaleGrace = 0
	st := core.NewState("traffic-incarnation", lim)
	at := time.Now().UTC().Add(-10 * time.Second)
	apply := func(op *core.Op, delta time.Duration) core.Result {
		t.Helper()
		r, _, err := st.Apply(op, at.Add(delta))
		if err != nil {
			t.Fatal("setup", op.Kind, err)
		}
		return r
	}
	apply(&core.Op{Kind: core.OpRegister, Name: "reused", NewToken: "old-token"}, 0)
	apply(&core.Op{Kind: core.OpRegister, Name: "reader", NewToken: "reader-token", Nonce: "reader-nonce", AgentKind: core.KindPersistent}, 0)
	apply(&core.Op{Kind: core.OpAckBoard, Token: "old-token"}, 0)
	apply(&core.Op{Kind: core.OpSendMessage, Token: "old-token", To: "reader", MsgType: core.MsgNotify, Body: "PRIVATE OLD INCARNATION", MailTrafficV1: true}, 0)
	old := st.Agents["reused"].CreatedSerial
	apply(&core.Op{Kind: core.OpSweep, StaleAgents: []string{"reused"}}, time.Second)
	apply(&core.Op{Kind: core.OpSweep}, 2*time.Second)
	apply(&core.Op{Kind: core.OpSweep, PurgeMail: true}, 3*time.Second)
	if st.Agents["reused"] != nil {
		t.Fatal("setup: prior incarnation not GC'd")
	}
	apply(&core.Op{Kind: core.OpRegister, Name: "reused", NewToken: "new-token"}, 4*time.Second)
	current := st.Agents["reused"].CreatedSerial
	if old == current {
		t.Fatal("setup: incarnation did not change")
	}
	apply(&core.Op{Kind: core.OpGrantRole, To: "reader", Mode: core.RoleCoordinator}, 4*time.Second)
	e := New(st, &memLedger{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	before, err := e.query(ctx, func() core.Result { return core.Result{"seen": e.seen["reused"]} })
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.TrafficCensus(ctx, "new-token", true, "")
	if err != nil {
		t.Fatal(err)
	}
	v := r["traffic"].(core.TrafficView)
	for _, d := range v.Days {
		if len(d.Agents) != 0 {
			t.Fatalf("new owner read old holder: %+v", d.Agents)
		}
	}
	r, err = e.TrafficCensus(ctx, "reader-token", true, "reused")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, d := range r["traffic"].(core.TrafficView).Days {
		for _, row := range d.Agents {
			if row.CreatedSerial != old || row.Sent.Notify != 1 {
				t.Fatalf("coordinator history lost: %+v", row)
			}
			n++
		}
	}
	if n != 1 {
		t.Fatal("setup: no old row available, fence test proves nothing")
	}
	observed, err := e.query(ctx, func() core.Result { return core.Result{"seen": e.seen["reused"]} })
	if err != nil || observed["seen"] != before["seen"] {
		t.Fatal("traffic read marked model contact", observed, err)
	}
	if _, err = e.TrafficCensus(ctx, "old-token", true, ""); err == nil {
		t.Fatal("old credential still reads")
	}
	if _, err = e.TrafficCensus(ctx, "new-token", false, ""); err == nil {
		t.Fatal("traffic granted body census")
	}
}
