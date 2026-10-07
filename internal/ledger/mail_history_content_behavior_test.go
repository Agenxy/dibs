package ledger

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/mailhistory"
)

type nativeContentFixture struct {
	l      *Ledger
	path   string
	serial uint64
	seek   mailhistory.SeekRange
}

// All interval metadata comes from real native appends, not a fabricated line.
func nativeContentInterval(t *testing.T) nativeContentFixture {
	t.Helper()
	l, path := newLedger(t)
	s := core.NewState("test", core.DefaultLimits())
	apply(t, s, l, &core.Op{Kind: core.OpRegister, Name: "alpha", NewToken: "alpha-token"}, t0)
	apply(t, s, l, &core.Op{Kind: core.OpRegister, Name: "bravo", NewToken: "bravo-token"}, t0)
	start, err := l.f.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatal("setup: native append position:", err)
	}
	anchor := mailhistory.Anchor{Serial: s.Serial + 1, Offset: start, Prev: l.headSum}
	op := &core.Op{Kind: core.OpSendMessage, Token: "alpha-token", To: "bravo", MsgType: core.MsgNotify, Body: "PRIVATE-NATIVE-HISTORY"}
	if err := s.Admit(op); err != nil {
		t.Fatal("setup: valid message refused:", err)
	}
	serial := apply(t, s, l, op, t0)["msg_serial"].(uint64)
	apply(t, s, l, &core.Op{Kind: core.OpUpdate, Token: "alpha-token", Description: "later record"}, t0)
	end, err := l.f.Seek(0, io.SeekCurrent)
	if err != nil || serial != anchor.Serial || end <= start {
		t.Fatalf("setup: invalid actual append interval: serial=%d start=%d end=%d err=%v", serial, start, end, err)
	}
	return nativeContentFixture{l: l, path: path, serial: serial, seek: mailhistory.SeekRange{Start: anchor, End: end, Hash: l.headSum}}
}

func TestNativeHistoryReadDecryptsWithoutMovingAppendPosition(t *testing.T) {
	f := nativeContentInterval(t)
	raw, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal("setup: native encrypted bytes:", err)
	}
	if bytes.Contains(raw, []byte("PRIVATE-NATIVE-HISTORY")) {
		t.Fatal("the native ledger exposed the private body")
	}
	op, err := f.l.ReadHistoryOp(context.Background(), f.serial, f.seek)
	if err != nil || op == nil || op.Kind != core.OpSendMessage || op.Body != "PRIVATE-NATIVE-HISTORY" {
		t.Fatalf("native quoted operation was not authenticated and decrypted: %v %v", op, err)
	}
	position, err := f.l.f.Seek(0, io.SeekCurrent)
	if err != nil || position != f.seek.End {
		t.Fatalf("content read changed writer append position: %d; want %d (%v)", position, f.seek.End, err)
	}
}

func TestNativeHistoryReadAuthenticatesAfterTheSelectedRecord(t *testing.T) {
	f := nativeContentInterval(t)
	raw, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal("setup: native bytes:", err)
	}
	// Change only the final operation's kind, keeping valid JSON and byte length.
	// The selected message and its ciphertext remain intact before that record.
	i := bytes.LastIndex(raw, []byte("update"))
	if i <= int(f.seek.Start.Offset) {
		t.Fatal("setup: later real update was not appended")
	}
	raw[i] = 'U'
	if err := os.WriteFile(f.path, raw, 0o600); err != nil {
		t.Fatal("setup: tamper later record:", err)
	}
	if op, err := f.l.ReadHistoryOp(context.Background(), f.serial, f.seek); op != nil || !errors.Is(err, mailhistory.ErrUnavailable) {
		t.Fatalf("selected content escaped before the later interval hash was authenticated: op=%v err=%v", op, err)
	}
}

func TestNativeHistoryReadRefusesOversizedIntervals(t *testing.T) {
	f := nativeContentInterval(t)
	f.seek.End = f.seek.Start.Offset + historySeekBytes + 1
	if op, err := f.l.ReadHistoryOp(context.Background(), f.serial, f.seek); op != nil || !errors.Is(err, mailhistory.ErrContentBudget) {
		t.Fatalf("oversized native read was not refused by the content budget: op=%v err=%v", op, err)
	}
}

func TestNativeHistoryReadHonorsCancellationBeforeDecoding(t *testing.T) {
	f := nativeContentInterval(t)
	f.seek.Start.Serial++ // the interval would fail its chain check if decoded
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if op, err := f.l.ReadHistoryOp(ctx, f.serial, f.seek); op != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled content read decoded the interval instead: op=%v err=%v", op, err)
	}
}
