// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

type retainedMailFixture struct {
	e         *Engine
	ctx       context.Context
	sender    string
	recipient string
}

func newRetainedMailFixture(t *testing.T, limits core.Limits) retainedMailFixture {
	t.Helper()
	e := New(core.NewState("retained-mail", limits), &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	joined := make(chan struct{})
	go func() { e.Run(ctx); close(joined) }()
	t.Cleanup(func() { cancel(); <-joined })
	stopWakeTimersOnCleanup(t, e)
	f := retainedMailFixture{e: e, ctx: ctx}
	f.sender = f.do(t, &core.Op{Kind: core.OpRegister, Name: "sender", Nonce: "retained-mail-sender"})["token"].(string)
	f.recipient = f.do(t, &core.Op{Kind: core.OpRegister, Name: "recipient", Nonce: "retained-mail-recipient"})["token"].(string)
	f.do(t, &core.Op{Kind: core.OpAckBoard, Token: f.sender})
	f.do(t, &core.Op{Kind: core.OpAckBoard, Token: f.recipient})
	return f
}

func (f retainedMailFixture) do(t *testing.T, op *core.Op) core.Result {
	t.Helper()
	r, err := f.e.Do(f.ctx, op)
	if err != nil || r["error"] != nil {
		t.Fatalf("setup %s failed: %v / %v", op.Kind, r, err)
	}
	return r
}

func (f retainedMailFixture) sendAndRead(t *testing.T, body string) uint64 {
	t.Helper()
	r := f.do(t, &core.Op{Kind: core.OpSendMessage, Token: f.sender, To: "recipient", MsgType: core.MsgNotify, Body: body})
	serial, ok := r["msg_serial"].(uint64)
	if !ok || serial == 0 {
		t.Fatalf("setup send returned no message serial: %v", r)
	}
	r, err := f.e.GetMessage(f.ctx, f.recipient, serial)
	m, ok := r["message"].(*core.Message)
	if err != nil || !ok || m == nil || m.Body != body {
		t.Fatalf("setup recipient could not read the retained envelope: %v / %v", r, err)
	}
	f.do(t, &core.Op{Kind: core.OpAckMessage, Token: f.recipient, MsgSerial: serial})
	return serial
}

func missingRetainedMail(t *testing.T, f retainedMailFixture, token string, serial uint64) *core.Error {
	t.Helper()
	r, err := f.e.GetMessage(f.ctx, token, serial)
	ce, _ := r["error"].(*core.Error)
	if ce == nil {
		_ = errors.As(err, &ce)
	}
	if ce == nil || ce.Code != "E_NO_MESSAGE" || r["message"] != nil {
		t.Fatalf("missing envelope was not refused: %v / %v", r, err)
	}
	return ce
}

func requireRetainedMailRecovery(t *testing.T, ce *core.Error) {
	t.Helper()
	for _, want := range []string{"retained", "retention", "inbox()", "mail_history("} {
		if !strings.Contains(ce.Hint, want) {
			t.Errorf("missing-envelope hint omits %q: %s", want, ce.Hint)
		}
	}
	if strings.Contains(ce.Hint, "the message was evicted") {
		t.Errorf("a missing serial does not prove that a message existed and was evicted: %s", ce.Hint)
	}
}

// The report was an acknowledged notify removed by ordinary consumed-mail GC,
// which does not advance the terminal-cap watermark. Exercise send/read/ack
// and the ledgered sweep before asking the real read_mail entry point again.
// A never-used serial must get the same explanation: no tombstone proves why
// either envelope is absent, so the hint cannot assert an exact cause.
func TestReadMailAfterConsumedRetentionOffersRecoveryWithoutInventingACause(t *testing.T) {
	limits := core.DefaultLimits()
	f := newRetainedMailFixture(t, limits)
	const body = "PRIVATE-RETAINED-NOTIFY"
	serial := f.sendAndRead(t, body)
	if r, err := f.e.GetMessage(f.ctx, f.sender, serial); err != nil || r["message"] == nil {
		t.Fatalf("setup sender lost its retained read window: %v / %v", r, err)
	}
	r, err := f.e.query(f.ctx, func() core.Result {
		m := f.e.state.Messages[serial]
		if m == nil || !m.Terminal() || !m.Consumed {
			return core.Result{"setup_failed": true}
		}
		later := m.TerminalAt.Add(limits.ConsumedRetention + time.Second)
		if !m.RetainUntil.IsZero() {
			later = m.RetainUntil.Add(time.Second)
		}
		if _, sweepErr := f.e.applyAndLedger(&core.Op{
			Kind: core.OpSweep, PurgeMail: true, V7Semantics: true, KeepArchivedNonce: true, KeepOwed: true,
		}, later); sweepErr != nil {
			return core.Result{"error": sweepErr}
		}
		return core.Result{"removed": f.e.state.Messages[serial] == nil, "watermark": f.e.state.Agents["recipient"].TruncatedBefore}
	})
	if err != nil || r["error"] != nil || r["removed"] != true || r["watermark"] != uint64(0) {
		t.Fatalf("setup did not remove consumed mail without a loss watermark: %v / %v", r, err)
	}
	for _, token := range []string{f.sender, f.recipient} {
		gone := missingRetainedMail(t, f, token, serial)
		unknown := missingRetainedMail(t, f, token, 1<<40)
		requireRetainedMailRecovery(t, gone)
		requireRetainedMailRecovery(t, unknown)
		if gone.Hint != unknown.Hint || strings.Contains(gone.Msg+gone.Hint, body) {
			t.Errorf("absence invented a specific cause or disclosed the removed body: %v / %v", gone, unknown)
		}
	}
}

// A truncation watermark identifies a range, not proof that every serial in
// that range held a message. A real terminal-cap eviction must still guide
// recovery without calling an unrelated, never-mail serial an evicted letter.
func TestReadMailBelowRetentionWatermarkDoesNotInventAnEnvelope(t *testing.T) {
	limits := core.DefaultLimits()
	limits.TerminalRetention = 1
	f := newRetainedMailFixture(t, limits)
	first := f.sendAndRead(t, "older notify")
	second := f.sendAndRead(t, "newer notify")
	f.do(t, &core.Op{Kind: core.OpSweep, PurgeMail: true})
	r, err := f.e.query(f.ctx, func() core.Result {
		return core.Result{
			"first_removed": f.e.state.Messages[first] == nil, "second_kept": f.e.state.Messages[second] != nil,
			"watermark": f.e.state.Agents["recipient"].TruncatedBefore, "one_was_not_mail": f.e.state.Messages[1] == nil,
		}
	})
	watermark, ok := r["watermark"].(uint64)
	if err != nil || !ok || watermark <= first || r["first_removed"] != true || r["second_kept"] != true || r["one_was_not_mail"] != true {
		t.Fatalf("setup did not produce a real retention-cap watermark: %v / %v", r, err)
	}
	for _, serial := range []uint64{first, 1} {
		ce := missingRetainedMail(t, f, f.recipient, serial)
		requireRetainedMailRecovery(t, ce)
		if !strings.Contains(ce.Hint, "truncated_before_serial") {
			t.Errorf("loss watermark disappeared from the corrective hint: %v", ce)
		}
	}
}
