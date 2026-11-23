// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/codexipc"
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/testcodexipc"
	"github.com/agenxy/dibs/internal/testexec"
)

func TestGoneQueueThreadStopsRetryAndEscalatesThroughSend(t *testing.T) {
	for _, prior := range []int{0, spawnFailureCap - 1} {
		t.Run(fmt.Sprintf("prior-failures-%d", prior), func(t *testing.T) { goneQueueThreadScenario(t, prior, false) })
	}
	t.Run("historical-warning", func(t *testing.T) { goneQueueThreadScenario(t, 0, true) })
}

func TestFinishedExecWakeOnDaemonRestartNotifiesOnlySenders(t *testing.T) {
	t.Setenv("DIBS_DIR", t.TempDir())
	testcodexipc.Start(t, "no-owner", nil)
	t.Setenv("DIBS_TEST_QUEUE_THREAD_SOURCE", "exec")
	binary := filepath.Join(t.TempDir(), "codex")
	if b, err := exec.Command("go", "build", "-o", binary, "../wakeexec/testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("setup: build queue fixture: %v: %s", err, b)
	}
	testexec.PrepareExecutable(t, exec.Command(binary, "--version"))

	const node = "finished-exec-restart"
	workers := []string{"builder", "maintainer", "repairer"}
	seed := core.NewState(node, core.DefaultLimits())
	old := time.Now().Add(-5 * 24 * time.Hour)
	var records []*core.Op
	add := func(op *core.Op) core.Result {
		t.Helper()
		res, _, err := seed.Apply(op, old)
		if err != nil {
			t.Fatalf("setup %s: %v", op.Kind, err)
		}
		records = append(records, op)
		return res
	}
	add(&core.Op{Kind: core.OpRegister, Name: "sender", NewToken: "token-sender", Nonce: "nonce-sender"})
	add(&core.Op{Kind: core.OpRegister, Name: "other-sender", NewToken: "token-other", Nonce: "nonce-other"})
	add(&core.Op{Kind: core.OpRegister, Name: "coordinator", NewToken: "token-lead", Nonce: "nonce-lead"})
	add(&core.Op{Kind: core.OpGrantRole, To: "coordinator", Mode: core.RoleCoordinator})
	var requests, handoffs []uint64
	for i, worker := range workers {
		add(&core.Op{
			Kind: core.OpRegister, Name: worker, AgentKind: core.KindPersistent,
			NewToken: "token-" + worker, Nonce: "nonce-" + worker,
			SessionID: fmt.Sprintf("019ffe52-0eaf-7f60-81cc-%012d", i+1),
			Agent:     &core.AgentInfo{Harness: "Codex", Surface: harnessenv.ChatGPTApp},
		})
		request := add(&core.Op{
			Kind: core.OpSendMessage, Token: "token-sender", To: worker, MsgType: core.MsgRequest,
			Body: "private source body", DeliveryStart: true,
		})["msg_serial"].(uint64)
		requests = append(requests, request)
		handoff := add(&core.Op{
			Kind: core.OpSendMessage, Token: "token-sender", To: worker, MsgType: core.MsgHandoff,
			Body: "private source body",
		})["msg_serial"].(uint64)
		handoffs = append(handoffs, handoff)
	}
	other := add(&core.Op{
		Kind: core.OpSendMessage, Token: "token-other", To: workers[0], MsgType: core.MsgRequest,
		Body: "private source body", DeliveryStart: true,
	})["msg_serial"].(uint64)
	add(&core.Op{Kind: core.OpMarkDelivered, MsgSerials: handoffs})
	start := func(st *core.State) (*Engine, *memLedger, context.Context, func()) {
		led := &memLedger{}
		e := New(st, led, deadProber{})
		e.SetHumanNotifier(noContactDesktop{})
		e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{
			binary, "queue", "--thread", "{thread}", "--message", "{message}",
		}}})
		ctx, cancel := context.WithCancel(t.Context())
		go e.Run(ctx)
		stop := func() { cancel(); <-e.Done() }
		t.Cleanup(stop)
		if _, err := e.query(ctx, func() core.Result { return nil }); err != nil {
			t.Fatalf("startup: %v", err)
		}
		for _, worker := range workers {
			waitWakeDone(t, e, worker)
		}
		return e, led, ctx, stop
	}
	check := func(e *Engine, ctx context.Context) {
		t.Helper()
		if _, err := e.query(ctx, func() core.Result {
			for _, worker := range workers {
				observed := e.wakers.queueObserved[worker].observation
				if observed.ThreadSource != "exec" || observed.ThreadState != "gone" {
					t.Errorf("setup: %s did not exercise exec refusal on startup: %+v", worker, observed)
				}
			}
			if len(e.state.Contacts) != 0 {
				t.Errorf("finished exec runs raised %d person contact alerts", len(e.state.Contacts))
			}
			if n := len(e.state.Inbox("coordinator")) + len(e.notices["coordinator"]); n > 1 {
				t.Errorf("finished exec runs produced %d coordinator alerts", n)
			}
			checkNotice := func(sender, worker string, serials []uint64) {
				var notices []*core.Message
				for _, notice := range e.state.Inbox(sender) {
					if notice.Type == core.MsgNotify && notice.From == e.dibsRowLocked() &&
						strings.Contains(notice.Body, fmt.Sprintf("to %q", worker)) {
						notices = append(notices, notice)
					}
				}
				if len(notices) != 1 {
					t.Errorf("%s has %d notices for %s, want one", sender, len(notices), worker)
					return
				}
				notice := notices[0]
				if !strings.Contains(notice.Body, "finished one-shot Codex run") ||
					strings.Contains(notice.Body, "private source body") {
					t.Errorf("inaccurate or private notice: %q", notice.Body)
				}
				var want []string
				for _, serial := range serials {
					want = append(want, fmt.Sprintf("#%d", serial))
					if m := e.state.Messages[serial]; m == nil || m.FinishedWakeNotice != notice.Serial {
						t.Errorf("source %d does not point to shared notice %d", serial, notice.Serial)
					}
				}
				if !strings.Contains(notice.Body, strings.Join(want, ", ")) || strings.Count(notice.Body, "#") != len(want) {
					t.Errorf("notice %q does not list exactly %v", notice.Body, want)
				}
			}
			for i, worker := range workers {
				checkNotice("sender", worker, []uint64{requests[i], handoffs[i]})
			}
			checkNotice("other-sender", workers[0], []uint64{other})
			for _, serial := range append(append(slices.Clone(requests), handoffs...), other) {
				m := e.state.Messages[serial]
				kind, state := core.MsgRequest, core.MsgStatePending
				if slices.Contains(handoffs, serial) {
					kind, state = core.MsgHandoff, core.MsgStateDelivered
				}
				if m == nil || m.Consumed || m.State != state {
					t.Errorf("source %s %d did not retain its original state", kind, serial)
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	first, ledger, ctx, stop := start(seed)
	check(first, ctx)
	stop()

	replayed := core.NewState(node, core.DefaultLimits())
	for i, op := range append(records, ledger.ops...) {
		if _, _, err := replayed.Apply(op, old.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("replay %s: %v", op.Kind, err)
		}
	}
	second, _, secondCtx, _ := start(replayed)
	check(second, secondCtx)
}

func goneQueueThreadScenario(t *testing.T, priorFailures int, historicalWarning bool) {
	t.Helper()
	var logs economyLog
	previousLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLog) })
	testcodexipc.Start(t, "no-owner", nil)
	binary := filepath.Join(t.TempDir(), "codex")
	if b, err := exec.Command("go", "build", "-o", binary, "../wakeexec/testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("setup: queue fixture build %v: %s", err, b)
	}
	testexec.PrepareExecutable(t, exec.Command(binary, "--version"))
	t.Setenv("DIBS_TEST_QUEUE_ERROR_THREAD", testcodexipc.Thread)
	t.Setenv("DIBS_TEST_QUEUE_ERROR", "thread/queue/add failed: failed to read thread: invalid thread-store request: no rollout found for thread id "+testcodexipc.Thread+" (code -32603)")
	previous := shower
	shower = &harnessenv.Shower{Holds: func(string) bool { return true }, Open: func([]string) error { t.Error("failed queue opened the app"); return nil }}
	t.Cleanup(func() { shower = previous })
	ledger := &queueReplayLedger{}
	e, ctx, worker, sender := nativeAppEngineWithLedger(t, ledger)
	e.SetHumanNotifier(noContactDesktop{})
	e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{
		binary, "queue", "--thread", "{thread}", "--message", "{message}",
	}}})
	e.wakers.mu.Lock()
	e.wakers.fails = map[string]int{"worker": priorFailures}
	e.wakers.mu.Unlock()
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpSweep, StaleAgents: []string{"worker"}}); err != nil {
		t.Fatal("setup dormancy:", err)
	}
	if _, err := e.query(ctx, func() core.Result {
		l := e.state.Agents["worker"]
		if l.Status != core.StatusDormant || l.DormantSince.IsZero() {
			t.Errorf("setup: worker must have a recorded dormancy transition, got %s at %v", l.Status, l.DormantSince)
		}
		// The fixture registered just now. Supply the matching turn-end
		// evidence so recency does not stand in for a turn still running.
		e.noteWakeEnded("worker", testcodexipc.Thread)
		return nil
	}); err != nil {
		t.Fatal("setup:", err)
	}
	if historicalWarning {
		seedHistoricalMissingThreadWarning(t, e, ctx)
	}
	// Exercise the periodic sweep before send so its maintenance cannot pass
	// outside this test merely because setup finished within one tick.
	if _, err := e.query(ctx, func() core.Result { e.sweep(time.Now()); return nil }); err != nil {
		t.Fatal("setup sweep:", err)
	}
	send := func() {
		t.Helper()
		if _, err := e.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: sender, To: "worker", MsgType: core.MsgRequest, Body: "private request"}); err != nil {
			t.Fatal("setup send:", err)
		}
		waitWakeDone(t, e, "worker")
	}
	send()
	board, err := e.Board(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var row map[string]any
	for _, candidate := range board["agents"].([]map[string]any) {
		if candidate["id"] == "worker" {
			row = candidate
		}
	}
	queue, _ := row["queue_wake"].(core.Result)
	wake, _ := row["wake"].(string)
	if queue["thread_state"] != "gone" || !strings.Contains(wake, "gone") {
		t.Errorf("real missing-thread command did not mark the board route gone: %+v", row)
	}
	if alerts, _ := board["contact_alerts"].([]contactAlertView); len(alerts) == 0 {
		t.Errorf("missing thread did not enter the existing contact-alert path: %v", board["contact_alerts"])
	}
	// The explicit queue refusal is also a reload refusal. Enter the real
	// return pass with the same still-pending mail, not a manually set gone flag.
	previousEpoch, previousOwned := appRestartEpoch.Load(), appThreadOwned
	appRestartEpoch.Store(func() (string, bool) { return "app:B", true })
	appThreadOwned = codexipc.Owned
	t.Cleanup(func() { appRestartEpoch.Store(previousEpoch); appThreadOwned = previousOwned })
	e.afterAppReturn(ctx, "app:B", nil, 0)
	waitWakeDone(t, e, "worker")
	pending, err := e.PendingForHuman(ctx)
	if err != nil || len(pending) == 0 || pending[0].Contact == nil {
		t.Fatalf("missing-thread contact disappeared: %v %v", pending, err)
	}
	if hint := pending[0].Contact.OpenHint; !strings.Contains(hint, bindingRemedy) ||
		strings.Contains(hint, "codex://threads/") || pending[0].Contact.OpenURL != "" {
		t.Fatalf("missing-thread contact offers an unusable link instead of recovery: %+v", pending[0].Contact)
	}
	e.wakers.mu.Lock()
	retry := e.wakers.deferred["worker"]
	e.wakers.mu.Unlock()
	if retry != nil {
		t.Error("permanently missing thread armed a failed-wake retry")
	}
	// Drive the real retry decision explicitly: no wall-clock delay can hide
	// a queued retry, and later messages must not start another process either.
	if _, err := e.query(ctx, func() core.Result { e.retryWakeDecision("worker"); return nil }); err != nil {
		t.Fatal(err)
	}
	waitWakeDone(t, e, "worker")
	send()
	attempts, err := os.ReadFile(filepath.Join(os.Getenv("CODEX_HOME"), "queue-attempts"))
	if err != nil || len(strings.Fields(string(attempts))) != 1 {
		t.Errorf("permanently missing thread ran again: %q %v", attempts, err)
	}
	assertGoneBindingMail(t, e, ctx, ledger)
	// A new binding must not inherit the old thread's permanent failure.
	const replacement = "dc8fea77-2092-4c69-b810-c96a7bc5575d"
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpUpdate, Token: worker, SessionAlias: replacement, V7Semantics: true}); err != nil {
		t.Fatal("setup replacement binding:", err)
	}
	send()
	attempts, err = os.ReadFile(filepath.Join(os.Getenv("CODEX_HOME"), "queue-attempts"))
	if err != nil || !strings.Contains(string(attempts), replacement) {
		t.Fatalf("replacement thread inherited gone route: %q %v", attempts, err)
	}
	// Return to the same dead target after a real replacement wake. Its cached
	// observation has changed, so the queue refuses it again; ordinary op_id
	// dedup must suppress another round of recipient/sender warning mail.
	if _, err := e.BindSession(ctx, worker, testcodexipc.Thread); err != nil {
		t.Fatal("setup same dead target:", err)
	}
	send()
	attempts, err = os.ReadFile(filepath.Join(os.Getenv("CODEX_HOME"), "queue-attempts"))
	if err != nil || len(strings.Fields(string(attempts))) != 3 {
		t.Fatalf("setup: same dead binding was not attempted again: %q %v", attempts, err)
	}
	assertGoneBindingMail(t, e, ctx, ledger)
	for line := range strings.SplitSeq(logs.String(), "\n") {
		if strings.Contains(line, "could not record missing-thread mailbox warning") {
			t.Errorf("missing-thread retry reported a send failure: %s", line)
		}
	}
}

// Seed the #607 warning wording and identifier through ordinary send. #632
// changed the body while retaining the identifier for the same binding.
func seedHistoricalMissingThreadWarning(t *testing.T, e *Engine, ctx context.Context) {
	t.Helper()
	_, token, err := e.dibsAgent(ctx)
	if err != nil {
		t.Fatal("setup: daemon reporter:", err)
	}
	got, err := e.query(ctx, func() core.Result { return core.Result{"created": e.state.Agents["worker"].CreatedSerial} })
	if err != nil {
		t.Fatal("setup: worker incarnation:", err)
	}
	key := sha256.Sum256(fmt.Appendf(nil, "worker\x00%d\x00%s\x00sender", got["created"], testcodexipc.Thread))
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: token, To: "sender", MsgType: core.MsgNotify,
		OpID: fmt.Sprintf("wake-thread-gone:%x", key),
		Body: "The board cannot wake \"worker\": the app queue reported no rollout found for its bound thread. " +
			"No automatic retry will run for this binding. Unread mail remains in its mailbox. " + bindingRemedy,
	}); err != nil {
		t.Fatal("setup: historical warning:", err)
	}
}

// Enter through real send -> native no-owner -> queue's explicit no-rollout
// refusal. Mail survives replay without a transient notice or event-ring body.
func assertGoneBindingMail(t *testing.T, e *Engine, ctx context.Context, ledger *queueReplayLedger) {
	t.Helper()
	var records []queueReplayRecord
	var reporter string
	var liveSerial uint64
	_, err := e.query(ctx, func() core.Result {
		reporter = e.dibsRowLocked()
		assertGoneBindingInboxes(t, e.state, reporter)
		records = slices.Clone(ledger.records)
		liveSerial = e.state.Serial
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	st := core.NewState("native-app", core.DefaultLimits())
	for i, record := range records {
		var op core.Op
		if err := json.Unmarshal(record.raw, &op); err != nil {
			t.Fatalf("decode ledger op %d: %v", i, err)
		}
		if _, _, err := st.Apply(&op, record.at); err != nil {
			t.Fatalf("replay op %d (%s): %v", i, op.Kind, err)
		}
	}
	if st.Serial != liveSerial {
		t.Errorf("replay serial = %d, live serial = %d", st.Serial, liveSerial)
	}
	assertGoneBindingInboxes(t, st, reporter)
}

func assertGoneBindingInboxes(t *testing.T, st *core.State, reporter string) {
	t.Helper()
	for _, who := range []string{"worker", "sender"} {
		count := 0
		for _, m := range st.Inbox(who) {
			if m.From != reporter || !strings.Contains(m.Body, "no rollout found") {
				continue
			}
			count++
			if m.Type != core.MsgNotify || !strings.Contains(m.Body, "bind_session") ||
				strings.Contains(m.Body, "private request") {
				t.Errorf("unusable binding warning is not factual, corrective system mail: %+v", m)
			}
			if !strings.Contains(m.Body, "Without the nonce") || !strings.Contains(m.Body, `adopt:"<old id>"`) {
				t.Errorf("durable warning stranded %s without its nonce: %s", who, m.Body)
			}
		}
		if count != 1 {
			t.Errorf("%s has %d durable missing-thread warnings, want exactly one", who, count)
		}
	}
}
