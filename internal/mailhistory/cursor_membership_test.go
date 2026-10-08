// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mailhistory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestHistoryAuthenticatedCursorRejectsReferenceOutsideParty(t *testing.T) {
	// Deliberately sign an invalid reference with this real Index's private key
	// to exercise the inner defense, after the outer MAC has accepted it. This
	// is not an externally forgeable cursor or a claim about attacker authority.
	st := core.NewState("membership", core.DefaultLimits())
	index := New()
	projector := index.ReplayProjector()
	var scratch Snapshot
	now := time.Now().UTC()
	apply := func(op *core.Op) Record {
		t.Helper()
		if err := st.Admit(op); err != nil {
			t.Fatal("setup: admit:", err)
		}
		before := Capture(st, op, &scratch)
		_, events, err := st.Apply(op, now)
		if err != nil {
			t.Fatal("setup: fold:", err)
		}
		rec := Record{Serial: st.Serial, At: now, Offset: int64(st.Serial), End: int64(st.Serial + 1)}
		if err := projector.Observe(context.Background(), st.Serial-1, rec, before, st, op, events); err != nil {
			t.Fatal("setup: projection:", err)
		}
		return rec
	}
	for _, id := range []string{"sender", "recipient", "stranger"} {
		apply(&core.Op{Kind: core.OpRegister, Name: id, NewToken: id + "-token", Nonce: id + "-nonce", PID: 1, AgentKind: core.KindPersistent})
		apply(&core.Op{Kind: core.OpAckBoard, Token: id + "-token"})
	}
	last := apply(&core.Op{Kind: core.OpSendMessage, Token: "sender-token", To: "recipient", MsgType: core.MsgNotify, Body: "party evidence"})
	projector.Progress(last)
	status := index.Status()
	reader := *st.Agents["stranger"]
	u, err := (&unitReader{index: index}).get(0)
	if err != nil {
		t.Fatal("setup: actual projected reference:", err)
	}
	signed := index.encodeCursor(cursor{1, status.Generation, last.Serial, u.Position, 0}, reader)
	if _, err := index.readCursor(signed, reader); err != nil {
		t.Fatal("setup: real-key MAC was invalid:", err)
	}
	_, err = index.ReadPage(context.Background(), PageRequest{Reader: reader, Upper: last.Serial, Limit: 1, Cursor: signed, Deadline: time.Now().Add(time.Second)})
	if !errors.Is(err, ErrCursor) {
		t.Fatal("authenticated cursor bypassed party membership", err)
	}
}
