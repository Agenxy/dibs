// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

type retentionRecord struct {
	at time.Time
	op []byte
}

type retentionLedger struct{ records []retentionRecord }

func (l *retentionLedger) Append(_ uint64, at time.Time, op *core.Op) error {
	data, err := json.Marshal(op)
	if err == nil {
		l.records = append(l.records, retentionRecord{at, data})
	}
	return err
}

type retentionBoard struct {
	t            *testing.T
	e            *Engine
	ctx          context.Context
	led          *retentionLedger
	lead, worker string
	base         time.Time
}

func newRetentionBoard(t *testing.T) *retentionBoard {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	led := &retentionLedger{}
	e := New(core.NewState("retention", core.DefaultLimits()), led, deadProber{})
	done := make(chan struct{})
	go func() { defer close(done); e.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	b := &retentionBoard{t: t, e: e, ctx: ctx, led: led, base: time.Now()}
	b.lead = b.do(&core.Op{Kind: core.OpRegister, Name: "lead", Nonce: "retention-lead"})["token"].(string)
	b.worker = b.do(&core.Op{Kind: core.OpRegister, Name: "worker", Nonce: "retention-worker"})["token"].(string)
	return b
}

func (b *retentionBoard) do(op *core.Op) core.Result {
	b.t.Helper()
	r, err := b.e.Do(b.ctx, op)
	if err != nil {
		b.t.Fatalf("operation %s/%s: %v", op.Kind, op.Disposition, err)
	}
	return r
}

func (b *retentionBoard) sweep(after time.Duration) {
	b.t.Helper()
	_, err := b.e.query(b.ctx, func() core.Result {
		_, err := b.e.applyAndLedger(&core.Op{Kind: core.OpSweep, PurgeMail: true, V7Semantics: true, KeepOwed: true, KeepArchivedNonce: true}, b.base.Add(after))
		return core.Result{"error": err}
	})
	if err != nil {
		b.t.Fatal(err)
	}
}

func (b *retentionBoard) doAt(op *core.Op, after time.Duration) core.Result {
	b.t.Helper()
	r, err := b.e.query(b.ctx, func() core.Result {
		r, err := b.e.exec(op, b.base.Add(after))
		if err != nil {
			return core.Result{"error": err}
		}
		return r
	})
	if err != nil {
		b.t.Fatalf("operation at %s: %v", after, err)
	}
	return r
}

func (b *retentionBoard) hasMessage(parent uint64) bool {
	b.t.Helper()
	r, err := b.e.query(b.ctx, func() core.Result { return core.Result{"present": b.e.state.Messages[parent] != nil} })
	if err != nil {
		b.t.Fatal(err)
	}
	return r["present"].(bool)
}

func (b *retentionBoard) request() uint64 {
	b.t.Helper()
	parent := b.do(&core.Op{Kind: core.OpSendMessage, Token: b.lead, To: "worker", MsgType: core.MsgRequest, Body: "proof", Milestones: []string{"one", "two"}})["msg_serial"].(uint64)
	b.do(&core.Op{Kind: core.OpRespond, Token: b.worker, MsgSerial: parent, Disposition: "approve"})
	b.do(&core.Op{Kind: core.OpRespond, Token: b.worker, MsgSerial: parent, Disposition: "progress", Milestone: 1, Body: "original proof"})
	b.do(&core.Op{Kind: core.OpRespond, Token: b.worker, MsgSerial: parent, Disposition: "done", Body: "delivered", Deliverable: "original-artifact"})
	return parent
}

func (b *retentionBoard) assertReplay() {
	b.t.Helper()
	_, err := b.e.query(b.ctx, func() core.Result {
		st := core.NewState("retention", core.DefaultLimits())
		for _, record := range b.led.records {
			var op core.Op
			if err := json.Unmarshal(record.op, &op); err != nil {
				return core.Result{"error": err}
			}
			if _, _, err := st.Apply(&op, record.at); err != nil {
				return core.Result{"error": err}
			}
		}
		live, _ := json.Marshal(b.e.state)
		replayed, _ := json.Marshal(st)
		if string(live) != string(replayed) {
			b.t.Error("live state differs from the actual recorded-op replay")
		}
		return nil
	})
	if err != nil {
		b.t.Fatal(err)
	}
}

func TestAnsweredQuestionRemainsReadableForReview(t *testing.T) {
	b := newRetentionBoard(t)
	parent := b.do(&core.Op{Kind: core.OpSendMessage, Token: b.lead, To: "worker", MsgType: core.MsgQuestion, Body: "review?"})["msg_serial"].(uint64)
	b.do(&core.Op{Kind: core.OpRespond, Token: b.worker, MsgSerial: parent, Disposition: "answer", Body: "yes"})
	b.sweep(20 * time.Minute)
	if _, err := b.e.GetMessage(b.ctx, b.lead, parent); err != nil {
		t.Fatalf("answered question disappeared before review: %v", err)
	}
	b.assertReplay()
}

func TestFlaggedDoneRequestCanRecordCorrection(t *testing.T) {
	b := newRetentionBoard(t)
	parent := b.request()
	b.do(&core.Op{Kind: core.OpRespond, Token: b.lead, MsgSerial: parent, Disposition: "flag", Milestone: 1, Body: "fix the proof"})
	b.sweep(48 * time.Hour)
	if _, err := b.e.GetMessage(b.ctx, b.worker, parent); err != nil {
		t.Fatalf("flagged work disappeared: %v", err)
	}
	b.doAt(&core.Op{Kind: core.OpRespond, Token: b.worker, MsgSerial: parent, Disposition: "progress", Milestone: 1, Body: "corrected", Deliverable: "corrected-artifact"}, 48*time.Hour)
	_, err := b.e.query(b.ctx, func() core.Result {
		m := b.e.state.Messages[parent]
		if m.State != core.MsgStateDone || m.Response != "done: delivered" || m.Deliverable != "original-artifact" || m.Owed(time.Now()) {
			t.Error("correction changed the original verdict or reopened work debt")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	b.assertReplay()
}

func TestDoneRequestCorrectionRequiresAnUnresolvedFlag(t *testing.T) {
	b := newRetentionBoard(t)
	parent := b.request()
	progress := &core.Op{Kind: core.OpRespond, Token: b.worker, MsgSerial: parent, Disposition: "progress", Milestone: 1, Body: "corrected"}
	if _, err := b.e.Do(b.ctx, progress); err == nil {
		t.Fatal("unflagged completed task accepted progress")
	}
	b.do(&core.Op{Kind: core.OpRespond, Token: b.lead, MsgSerial: parent, Disposition: "flag", Milestone: 1, Body: "fix"})
	b.do(progress)
	if _, err := b.e.Do(b.ctx, progress); err == nil {
		t.Fatal("resolved completed task accepted more progress")
	}
	b.assertReplay()
}

func TestReviewRetentionStillHonorsTerminalCap(t *testing.T) {
	b := newRetentionBoard(t)
	var first, last uint64
	for i := 0; i <= core.DefaultLimits().TerminalRetention; i++ {
		after := time.Duration(i+1) * time.Second
		last = b.doAt(&core.Op{Kind: core.OpSendMessage, Token: b.lead, To: "worker", MsgType: core.MsgQuestion, Body: "review"}, after)["msg_serial"].(uint64)
		if i == 0 {
			first = last
		}
		b.doAt(&core.Op{Kind: core.OpRespond, Token: b.worker, MsgSerial: last, Disposition: "answer", Body: "yes"}, after)
	}
	b.sweep(20 * time.Minute)
	_, err := b.e.query(b.ctx, func() core.Result {
		if len(b.e.state.Messages) != core.DefaultLimits().TerminalRetention || b.e.state.Messages[first] != nil || b.e.state.Messages[last] == nil || b.e.state.Agents["worker"].TruncatedBefore < first {
			t.Error("retention bypassed the bounded oldest-first cap or loss watermark")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	b.assertReplay()
}

func TestWholeWorkFlagOnlyClearsByWholeWorkCorrectionOrAccept(t *testing.T) {
	for _, disposition := range []string{"progress", "accept"} {
		t.Run(disposition, func(t *testing.T) {
			b := newRetentionBoard(t)
			parent := b.request()
			b.do(&core.Op{Kind: core.OpRespond, Token: b.lead, MsgSerial: parent, Disposition: "flag", Body: "whole work needs correction"})
			b.do(&core.Op{Kind: core.OpRespond, Token: b.worker, MsgSerial: parent, Disposition: "progress", Milestone: 1, Body: "one step corrected"})
			b.do(&core.Op{Kind: core.OpRespond, Token: b.lead, MsgSerial: parent, Disposition: "accept", Milestone: 1})
			b.sweep(48 * time.Hour)
			if _, err := b.e.GetMessage(b.ctx, b.lead, parent); err != nil {
				t.Fatalf("partial review cleared whole-work flag: %v", err)
			}
			token := b.worker
			if disposition == "accept" {
				token = b.lead
			}
			b.doAt(&core.Op{Kind: core.OpRespond, Token: token, MsgSerial: parent, Disposition: disposition, Body: "whole work corrected"}, 48*time.Hour)
			b.sweep(71 * time.Hour)
			if !b.hasMessage(parent) {
				t.Fatal("review window ended early")
			}
			b.sweep(73 * time.Hour)
			if b.hasMessage(parent) {
				t.Fatal("resolved work retained beyond review window")
			}
			b.assertReplay()
		})
	}
}

func TestLegacyResponseReplayKeepsFifteenMinuteRetention(t *testing.T) {
	b := newRetentionBoard(t)
	parent := b.do(&core.Op{Kind: core.OpSendMessage, Token: b.lead, To: "worker", MsgType: core.MsgQuestion, Body: "old question"})["msg_serial"].(uint64)
	b.do(&core.Op{Kind: core.OpRespond, Token: b.worker, MsgSerial: parent, Disposition: "answer", Body: "old answer"})
	_, err := b.e.query(b.ctx, func() core.Result {
		st := core.NewState("retention", core.DefaultLimits())
		for _, record := range b.led.records {
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(record.op, &raw); err != nil {
				return core.Result{"error": err}
			}
			delete(raw, "retain_until") // the exact absent-field shape of old operations
			data, err := json.Marshal(raw)
			if err != nil {
				return core.Result{"error": err}
			}
			var op core.Op
			if err := json.Unmarshal(data, &op); err != nil {
				return core.Result{"error": err}
			}
			if _, _, err := st.Apply(&op, record.at); err != nil {
				return core.Result{"error": err}
			}
		}
		if !st.Messages[parent].RetainUntil.IsZero() {
			t.Error("legacy operation acquired new retention")
		}
		_, _, err := st.Apply(&core.Op{Kind: core.OpSweep, PurgeMail: true, V7Semantics: true, KeepOwed: true}, b.base.Add(20*time.Minute))
		if st.Messages[parent] != nil {
			t.Error("historical sweep changed its fifteen-minute decision")
		}
		return core.Result{"error": err}
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReviewRetentionIgnoresIngressDatesAndDoesNotAmplifySweeps(t *testing.T) {
	b := newRetentionBoard(t)
	parent := b.do(&core.Op{Kind: core.OpSendMessage, Token: b.lead, To: "worker", MsgType: core.MsgQuestion, Body: "question"})["msg_serial"].(uint64)
	forged := b.base.Add(100 * 24 * time.Hour)
	b.do(&core.Op{Kind: core.OpRespond, Token: b.worker, MsgSerial: parent, Disposition: "answer", Body: "answer", RetainUntil: &forged})
	var serial uint64
	var count int
	_, err := b.e.query(b.ctx, func() core.Result {
		m := b.e.state.Messages[parent]
		if !m.RetainUntil.Equal(m.TerminalAt.Add(24 * time.Hour)) {
			t.Error("caller chose its own retention")
		}
		if m.RetainUntil != m.RetainUntil.Round(0) || m.RetainUntil.Location() != time.UTC {
			t.Error("recorded deadline carries an unserialized clock or local zone")
		}
		serial, count = b.e.state.Serial, len(b.led.records)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		b.sweep(20 * time.Minute)
	}
	_, err = b.e.query(b.ctx, func() core.Result {
		if b.e.state.Serial != serial || len(b.led.records) != count {
			t.Error("unchanged sweeps ledgered retention repeatedly")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	b.sweep(25 * time.Hour)
	if _, err := b.e.GetMessage(b.ctx, b.lead, parent); err == nil {
		t.Fatal("answered question retained beyond one day")
	}
	b.assertReplay()
}
