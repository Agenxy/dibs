package mailhistory

import (
	"context"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestHistoryPendingMetadataPreservesIssuedCursor(t *testing.T) {
	// Drive the actual private projector with real canonical transitions. Its
	// decoded metadata may be visible before Progress publishes the batch head.
	st := core.NewState("page-progress", core.DefaultLimits())
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
	for _, id := range []string{"sender", "recipient"} {
		apply(&core.Op{Kind: core.OpRegister, Name: id, NewToken: id + "-token", Nonce: id + "-nonce", PID: 1, AgentKind: core.KindPersistent})
		apply(&core.Op{Kind: core.OpAckBoard, Token: id + "-token"})
	}
	first := apply(&core.Op{Kind: core.OpSendMessage, Token: "sender-token", To: "recipient", MsgType: core.MsgNotify, Body: "first"})
	apply(&core.Op{Kind: core.OpSendMessage, Token: "sender-token", To: "recipient", MsgType: core.MsgNotify, Body: "next batch"})
	projector.Progress(first)
	req := PageRequest{Reader: *st.Agents["sender"], Upper: st.Serial, Limit: 1, Deadline: time.Now().Add(-time.Second)}
	page, err := index.ReadPage(context.Background(), req)
	if err != nil || len(page.Units) != 1 || page.Next == "" {
		t.Fatal("setup: native numeric page:", err, page)
	}
	req.Cursor = page.Next
	pending, err := index.ReadPage(context.Background(), req)
	if err != nil || pending.Examined != 0 || len(pending.Units) != 0 || pending.Next != page.Next {
		t.Fatal("unexamined pending page discarded issued continuation:", err, pending)
	}
}
