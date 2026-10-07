package mailhistory_test

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
	"github.com/agenxy/dibs/internal/mailhistory"
)

// This observes the actual native writer observer input and then delegates
// unchanged to the real ledger view. No Capture call or history flag is set.
type capturedHistoryWriter struct {
	*ledger.Ledger
	checkpoint chan int
	armed      atomic.Bool
}

func (w *capturedHistoryWriter) ObserveMail(before mailhistory.Snapshot, st *core.State,
	op *core.Op, events []core.Event,
) {
	if w.armed.Load() && (op.Kind == core.OpSetSlot || op.Kind == core.OpSignOff || op.Kind == core.OpSendMessage || (op.Kind == core.OpAckBoard && op.AgentID == "sender")) {
		w.checkpoint <- len(before.Mail)
	}
	w.Ledger.ObserveMail(before, st, op, events)
}

func TestMailHistoryNativeNonMailWriterDoesNotCopyLiveMailbox(t *testing.T) {
	var writer *capturedHistoryWriter
	f := nativeHistory(t, t.TempDir(), func(l *ledger.Ledger) engine.Ledger {
		writer = &capturedHistoryWriter{Ledger: l, checkpoint: make(chan int, 1)}
		return writer
	})
	sender := historyIdentity(t, f, "sender")
	recipient := historyIdentity(t, f, "recipient")
	for n := 0; n < 200; n++ {
		historyOp(t, f, &core.Op{Kind: core.OpSendMessage, Token: sender, To: "recipient", MsgType: core.MsgNotify, Body: "live writer-cost evidence"})
	}
	// Real read-side metadata establishes that setup retained all 200 messages.
	inbox, err := f.eng.Inbox(f.ctx, recipient)
	if err != nil {
		t.Fatal("setup: actual inbox:", err)
	}
	messages, ok := inbox["messages"].([]*core.Message)
	if !ok || len(messages) != 200 {
		t.Fatal("setup: expected 200 live messages:", inbox)
	}
	writer.armed.Store(true)
	historyOp(t, f, &core.Op{Kind: core.OpSetSlot, Token: sender, Text: "actual mail-free writer"})
	select {
	case copied := <-writer.checkpoint:
		if copied != 0 {
			t.Fatalf("non-mail writer copied unrelated live mailbox: %d metadata rows", copied)
		}
	case <-f.ctx.Done():
		t.Fatal("setup: native committed observer did not run")
	}
	settledHistory(t, f, recipient, false)
}

func TestMailHistoryNativeCheckpointCopiesOwnMailboxOnly(t *testing.T) {
	var writer *capturedHistoryWriter
	f := nativeHistory(t, t.TempDir(), func(l *ledger.Ledger) engine.Ledger {
		writer = &capturedHistoryWriter{Ledger: l, checkpoint: make(chan int, 1)}
		return writer
	})
	sender := historyIdentity(t, f, "sender")
	recipient := historyIdentity(t, f, "recipient")
	for n := 0; n < 200; n++ {
		historyOp(t, f, &core.Op{Kind: core.OpSendMessage, Token: sender, To: "recipient", MsgType: core.MsgNotify, Body: "unrelated live mail"})
	}
	for n := 0; n < 20; n++ {
		historyOp(t, f, &core.Op{Kind: core.OpSendMessage, Token: recipient, To: "sender", MsgType: core.MsgNotify, Body: "own live mail"})
	}
	writer.armed.Store(true)
	result := historyOp(t, f, &core.Op{Kind: core.OpAckBoard, Token: sender})
	inbox, ok := result["inbox"].([]*core.Message)
	if !ok || len(inbox) != 20 {
		t.Fatal("setup: own checkpoint mailbox:", result)
	}
	for _, m := range inbox {
		if m.State != core.MsgStateDelivered {
			t.Fatal("checkpoint omitted actual delivery transition:", m)
		}
	}
	select {
	case copied := <-writer.checkpoint:
		if copied != 20 {
			t.Fatalf("checkpoint copied unrelated board mail: %d rows for 20 own messages", copied)
		}
	case <-f.ctx.Done():
		t.Fatal("setup: committed checkpoint observer did not run")
	}
	settledHistory(t, f, sender, false)
}

func TestMailHistoryNativeFailedBuilderStopsWriterSnapshots(t *testing.T) {
	dir := t.TempDir()
	seedWriterCostLedger(t, dir)
	var writer *capturedHistoryWriter
	f := nativeHistory(t, dir, func(l *ledger.Ledger) engine.Ledger {
		// Replay has validated the real ledger; corrupt a native reader byte before
		// first serving Accept. The actual private fold must fail, without a setter.
		file, err := os.OpenFile(filepath.Join(dir, "ledger.jsonl"), os.O_WRONLY, 0)
		if err != nil {
			t.Fatal("setup: native corruption:", err)
		}
		_, err = file.WriteAt([]byte("!"), 0)
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			t.Fatal("setup: native corruption:", err, closeErr)
		}
		writer = &capturedHistoryWriter{Ledger: l, checkpoint: make(chan int, 1)}
		return writer
	})
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !f.led.MailHistory().Status().Failed {
		select {
		case <-tick.C:
		case <-timer.C:
			t.Fatal("setup: corrupt private native fold did not fail")
		}
	}
	f.ids["sender-token"] = "sender"
	writer.armed.Store(true)
	historyOp(t, f, &core.Op{Kind: core.OpSignOff, Token: "sender-token"})
	select {
	case copied := <-writer.checkpoint:
		if copied != 0 {
			t.Fatalf("failed history kept copying writer metadata: %d rows", copied)
		}
	case <-f.ctx.Done():
		t.Fatal("setup: ordinary committed writer stopped after history failure")
	}
}

func seedWriterCostLedger(t *testing.T, dir string) {
	t.Helper()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal("setup:", err)
	}
	l, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "history-native", box)
	if err != nil {
		t.Fatal("setup:", err)
	}
	defer func() {
		if err := l.Close(); err != nil {
			t.Error(err)
		}
	}()
	state := core.NewState("history-native", core.DefaultLimits())
	writer := historyBudgetWriter{t: t, state: state, ledger: l, now: time.Now().UTC()}
	for _, id := range []string{"sender", "recipient"} {
		writer.apply(&core.Op{Kind: core.OpRegister, Name: id, Nonce: id + "-nonce", NewToken: id + "-token", PID: 1, AgentKind: core.KindPersistent})
		writer.apply(&core.Op{Kind: core.OpAckBoard, Token: id + "-token"})
	}
	for n := 0; n < 20; n++ {
		writer.apply(&core.Op{Kind: core.OpSendMessage, Token: "sender-token", To: "recipient", MsgType: core.MsgNotify, Body: "live failed-builder evidence"})
	}
}

func TestMailHistoryNativeNamedSendCapturesDisplacedRecipientMail(t *testing.T) {
	limits := core.DefaultLimits()
	limits.MaxMailboxDepth = 2
	var writer *capturedHistoryWriter
	f := nativeHistoryWithLimits(t, t.TempDir(), limits, func(l *ledger.Ledger) engine.Ledger {
		writer = &capturedHistoryWriter{Ledger: l, checkpoint: make(chan int, 1)}
		return writer
	})
	sender := historyIdentity(t, f, "sender")
	recipient := historyIdentity(t, f, "recipient")
	historyOp(t, f, &core.Op{Kind: core.OpUpdate, Token: recipient, Name: "recipient-visible-name"})
	for n := 0; n < 2; n++ {
		historyOp(t, f, &core.Op{Kind: core.OpSendMessage, Token: sender, To: "recipient", MsgType: core.MsgNotify, Body: "displacement setup"})
	}
	writer.armed.Store(true)
	op := &core.Op{Kind: core.OpSendMessage, Token: sender, To: "recipient-visible-name", MsgType: core.MsgNotify, Body: "named ingress evidence"}
	historyOp(t, f, op)
	writer.armed.Store(false)
	if op.To != f.ids[recipient] {
		t.Fatal("setup: native named ingress did not canonicalize recipient", op.To)
	}
	select {
	case copied := <-writer.checkpoint:
		if copied != 2 {
			t.Fatal("named send omitted displaced recipient snapshot", copied)
		}
	case <-f.ctx.Done():
		t.Fatal("setup: actual send observer did not run")
	}
	res := settledHistory(t, f, sender, false)
	if len(historyUnits(t, res)) < 4 {
		t.Fatal("named displacement lost canonical transition", res)
	}
}

func TestMailHistoryNativeAuthorMachineIsEventTimeEvidence(t *testing.T) {
	f := nativeHistory(t, t.TempDir())
	sender := historyIdentity(t, f, "sender")
	historyIdentity(t, f, "recipient")
	historyOp(t, f, &core.Op{Kind: core.OpUpdate, Token: sender, Agent: &core.AgentInfo{HostID: "machine-before"}})
	historyOp(t, f, &core.Op{Kind: core.OpSendMessage, Token: sender, To: "recipient", MsgType: core.MsgNotify, Body: "before identity update"})
	historyOp(t, f, &core.Op{Kind: core.OpUpdate, Token: sender, Name: "sender-renamed", Agent: &core.AgentInfo{HostID: "machine-after"}})
	historyOp(t, f, &core.Op{Kind: core.OpSendMessage, Token: sender, To: "recipient", MsgType: core.MsgNotify, Body: "after identity update"})
	rows := historyUnits(t, settledHistory(t, f, sender, false))
	if len(rows) != 2 {
		t.Fatal("setup: authored unit count", rows)
	}
	first, ok := rows[0]["unit"].(mailhistory.Unit)
	if !ok {
		t.Fatal("setup: typed projected unit", rows[0])
	}
	second := rows[1]["unit"].(mailhistory.Unit)
	if first.Author.Host != "machine-before" || second.Author.Host != "machine-after" || first.Author.ID != second.Author.ID {
		t.Fatal("identity update rewrote event-time attribution", first.Author, second.Author)
	}
}
