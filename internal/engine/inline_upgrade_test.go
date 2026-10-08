// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/ledger"
)

// Opt-in because this fixture is a PRIVATE COPY of an encrypted pre-upgrade
// board. Each run copies the original encrypted prefix again, including when
// an earlier probe appended its upgrade cutoff. Never use a running board.
func TestReviewReadUpgradeOnCopiedRealLedger(t *testing.T) {
	dir := os.Getenv("DIBS_TEST_PREUPGRADE_LEDGER_DIR")
	if dir == "" {
		t.Skip("requires copied pre-upgrade ledger and key")
	}
	if !strings.HasPrefix(filepath.Base(dir), "dibs-inline-upgrade.") {
		t.Fatal("fixture must be an explicitly named private upgrade copy, never the live board")
	}
	for _, name := range []string{"key", "ledger.jsonl"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("fixture missing %s", name)
		}
	}
	dir = copyPreUpgradeLedger(t, dir)
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "upgrade-proof", box)
	if err != nil {
		t.Fatal(err)
	}
	st := core.NewState("upgrade-proof", core.DefaultLimits())
	if _, err := journal.Replay(st); err != nil {
		t.Fatal(err)
	}
	if st.ReviewReadCutoff != 0 {
		t.Fatal("fixture is not pre-upgrade")
	}
	before := st.Serial
	retainedReviews, retainedProgress := 0, 0
	for _, m := range st.Messages {
		if (m.State == core.MsgStateApproved || m.State == core.MsgStateDone) && m.RetainUntil.After(time.Now()) {
			for _, p := range m.Progress {
				if p.Review != "" {
					retainedReviews++
				} else {
					retainedProgress++
				}
			}
		}
	}
	if retainedReviews == 0 || retainedProgress == 0 {
		t.Fatal("fixture must retain historical reviews and progress; zero-flood result would prove nothing")
	}
	// The originally reported parent mixes already-read legacy outcomes with
	// later progress. Keep using that exact history, not a fresh synthetic parent.
	parent := st.Messages[23356]
	if parent == nil || parent.State != core.MsgStateApproved || parent.OutcomeReadAt == 0 ||
		parent.LatestOutcomeSerial() <= parent.OutcomeReadAt {
		t.Fatal("fixture lacks the retained legacy-read request with later progress")
	}
	lead, worker := st.Agents[parent.From], st.Agents[parent.To]
	if lead == nil || worker == nil || lead.Retired() || worker.Retired() {
		t.Fatal("fixture lacks the original request's available participants")
	}
	leadToken, workerToken := lead.Token, worker.Token
	e := New(st, journal, nil)
	e.SetSocketWakes(false) // no sockets/commands configured or delivered by this fixture
	e.SetWakePolicy(WakeNone)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	unreadVerdicts := 0
	_, err = e.query(ctx, func() core.Result {
		if e.state.ReviewReadCutoff != before+1 {
			t.Error("production Run did not record the upgrade cutoff")
		}
		unreadVerdicts = assertHistoricalUpgradeUnits(t, e)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFreshUpgradeProgress(t, e, ctx, leadToken, workerToken, parent.Serial)
	cancel()
	<-done
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal, err = ledger.Open(filepath.Join(dir, "ledger.jsonl"), "upgrade-proof", box)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := journal.Close(); err != nil {
			t.Error(err)
		}
	}()
	replayedState := core.NewState("upgrade-proof", core.DefaultLimits())
	if _, err := journal.Replay(replayedState); err != nil {
		t.Fatal(err)
	}
	if replayedState.ReviewReadCutoff != before+1 {
		t.Fatal("upgrade cutoff was only in memory")
	}
	if !t.Failed() {
		t.Logf("pre-upgrade serial=%d; retained historical reviews=%d progress=%d; old progress/review redeliveries=0; preserved unread legacy verdicts=%d; fresh progress exactly once; cutoff=%d survives encrypted replay", before, retainedReviews, retainedProgress, unreadVerdicts, replayedState.ReviewReadCutoff)
	}
}

func copyPreUpgradeLedger(t *testing.T, source string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(source, "ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var prefix []byte
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var record struct {
			Kind string `json:"e"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal("invalid encrypted fixture record:", err)
		}
		if record.Kind == core.OpInitializeReviewRead {
			break
		}
		prefix = append(prefix, line...) // preserve raw hash-chained bytes
	}
	key, err := os.ReadFile(filepath.Join(source, "key"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, content := range map[string][]byte{"key": key, "ledger.jsonl": prefix} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func assertFreshUpgradeProgress(t *testing.T, e *Engine, ctx context.Context, lead, worker string, parent uint64) {
	t.Helper()
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRespond, Token: worker, MsgSerial: parent,
		Disposition: "progress", Body: "post-cutoff-progress-proof", Milestone: 1,
	}); err != nil {
		t.Fatal("post-cutoff progress setup:", err)
	}
	for read := range 2 {
		r, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: lead})
		if err != nil {
			t.Fatal("post-cutoff progress delivery:", err)
		}
		lines := r["agent_updates"].([]string)
		if read == 0 {
			if len(lines) != 1 || !strings.Contains(lines[0], "post-cutoff-progress-proof") {
				t.Errorf("first post-cutoff pull: want only the fresh unit, got %d units", len(lines))
			}
		} else if len(lines) != 0 {
			t.Errorf("read progress repeated on second pull: %d units", len(lines))
		}
	}
}

func TestUpgradeReadCutoffSuppressesHistoricalSenderUnits(t *testing.T) {
	for _, method := range []string{"unread", "read_mail", "check_in"} {
		t.Run(method, func(t *testing.T) {
			legacyRead := method == "read_mail"
			st := core.NewState("upgrade-outcomes", core.DefaultLimits())
			now := time.Now()
			apply := func(op *core.Op) core.Result {
				t.Helper()
				r, _, err := st.Apply(op, now)
				if err != nil {
					t.Fatalf("pre-upgrade setup %s: %v", op.Kind, err)
				}
				return r
			}
			for _, id := range []string{"lead", "worker"} {
				apply(&core.Op{Kind: core.OpRegister, Name: id, NewToken: id + "-token", Nonce: "upgrade-" + id})
				apply(&core.Op{Kind: core.OpAckBoard, Token: id + "-token"})
			}
			parent := apply(&core.Op{
				Kind: core.OpSendMessage, Token: "lead-token", To: "worker",
				MsgType: core.MsgRequest, Body: "upgrade work", Milestones: []string{"proof"},
			})["msg_serial"].(uint64)
			apply(&core.Op{Kind: core.OpRespond, Token: "worker-token", MsgSerial: parent, Disposition: "approve", Body: "old approval"})
			if legacyRead {
				apply(&core.Op{Kind: core.OpOutcomeRead, Token: "lead-token", MsgSerial: parent})
			}
			if method == "check_in" {
				apply(&core.Op{Kind: core.OpAckBoard, Token: "lead-token"})
			}
			apply(&core.Op{Kind: core.OpRespond, Token: "worker-token", MsgSerial: parent, Disposition: "progress", Body: "old progress", Milestone: 1})
			if legacyRead {
				beforeRead := st.Messages[parent].OutcomeReadAt
				apply(&core.Op{Kind: core.OpOutcomeRead, Token: "lead-token", MsgSerial: parent})
				if st.Messages[parent].OutcomeReadAt != beforeRead {
					t.Fatal("setup changed historical zero-prefix fold semantics")
				}
			}
			apply(&core.Op{Kind: core.OpRespond, Token: "lead-token", MsgSerial: parent, Disposition: "accept", Milestone: 1, Body: "old review"})
			before := st.Serial
			e := New(st, &memLedger{}, deadProber{})
			e.SetSocketWakes(false)
			e.SetWakePolicy(WakeNone)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { e.Run(ctx); close(done) }()
			t.Cleanup(func() { cancel(); <-done })
			if _, err := e.query(ctx, func() core.Result {
				if e.state.ReviewReadCutoff != before+1 {
					t.Error("production boot did not record the upgrade read cutoff")
				}
				assertHistoricalUpgradeUnits(t, e)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			// An approval never read before the upgrade is still due, unlike
			// the old progress whose receipts were not persisted.
			r, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: "lead-token"})
			if err != nil {
				t.Fatal(err)
			}
			lines := r["agent_updates"].([]string)
			want := 0
			if method == "unread" {
				want = 1
			}
			if len(lines) != want || (want == 1 && !strings.Contains(lines[0], "old approval")) {
				t.Errorf("upgrade dropped an unread approval or replayed progress: got %d units, want %d", len(lines), want)
			}
			assertFreshUpgradeProgress(t, e, ctx, "lead-token", "worker-token", parent)
		})
	}
}

func assertHistoricalUpgradeUnits(t *testing.T, e *Engine) int {
	t.Helper()
	want := map[uint64]bool{}
	for _, m := range e.state.Messages {
		l := e.state.Agents[m.From]
		if l == nil || l.Retired() || (l.CreatedSerial > 0 && m.Serial < l.CreatedSerial) {
			continue
		}
		if e.stillOwed(m) {
			want[m.RespondedAt] = true
		}
		if m.State == core.MsgStateQueued && m.QueueChangedSerial > m.RespondedAt &&
			m.QueueChangedSerial > m.OutcomeReadAt && m.QueueChangedSerial > l.AckedSerial {
			want[m.QueueChangedSerial] = true
		}
	}
	seen := map[uint64]bool{}
	for id := range e.state.Agents {
		for _, group := range e.outcomeGroups(id) {
			for _, unit := range group.units {
				switch {
				case unit.kind == "message.progress" || unit.kind == "message.review":
					t.Error("historical progress or review would redeliver at upgrade")
				case !want[unit.serial]:
					t.Error("a previously read historical verdict/queue unit would redeliver")
				default:
					seen[unit.serial] = true
				}
			}
		}
	}
	if len(seen) != len(want) {
		t.Errorf("upgrade lost durable unread legacy units: got %d, want %d", len(seen), len(want))
	}
	return len(seen)
}

func TestUpgradeReadCutoffPreservesUnreadLegacyVerdicts(t *testing.T) {
	for _, kind := range []string{"answer", "queue change"} {
		t.Run(kind, func(t *testing.T) {
			st := core.NewState("upgrade-unread", core.DefaultLimits())
			apply := func(op *core.Op) core.Result {
				t.Helper()
				r, _, err := st.Apply(op, time.Now())
				if err != nil {
					t.Fatalf("pre-upgrade setup %s: %v", op.Kind, err)
				}
				return r
			}
			for _, id := range []string{"lead", "worker"} {
				apply(&core.Op{Kind: core.OpRegister, Name: id, NewToken: id + "-token", Nonce: "unread-" + id})
				apply(&core.Op{Kind: core.OpAckBoard, Token: id + "-token"})
			}
			msgType, disposition, body := core.MsgQuestion, "answer", "legacy-unread-answer"
			if kind == "queue change" {
				msgType, disposition, body = core.MsgRequest, "queue", "queue accepted"
			}
			parent := apply(&core.Op{
				Kind: core.OpSendMessage, Token: "lead-token", To: "worker",
				MsgType: msgType, Body: "work", QueueDebt: kind == "queue change",
			})["msg_serial"].(uint64)
			apply(&core.Op{
				Kind: core.OpRespond, Token: "worker-token", MsgSerial: parent,
				Disposition: disposition, Body: body, QueueDebt: kind == "queue change",
			})
			want := body
			if kind == "queue change" {
				apply(&core.Op{Kind: core.OpOutcomeRead, Token: "lead-token", MsgSerial: parent})
				apply(&core.Op{Kind: core.OpQueueUpdate, Token: "worker-token", MsgSerial: parent, QueuePriority: "urgent"})
				want = "Queue position or priority changed"
			}
			e := New(st, &memLedger{}, deadProber{})
			e.SetSocketWakes(false)
			e.SetWakePolicy(WakeNone)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { e.Run(ctx); close(done) }()
			t.Cleanup(func() { cancel(); <-done })
			for read := range 2 {
				r, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: "lead-token"})
				if err != nil {
					t.Fatal(err)
				}
				lines := r["agent_updates"].([]string)
				if read == 0 && (len(lines) != 1 || !strings.Contains(lines[0], want)) {
					t.Errorf("genuinely unread pre-upgrade %s lost: got %d units", kind, len(lines))
				} else if read == 1 && len(lines) != 0 {
					t.Errorf("durably read %s repeated", kind)
				}
			}
		})
	}
}
