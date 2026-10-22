// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/testcodexipc"
	"github.com/agenxy/dibs/internal/testexec"
)

func TestGoneQueueThreadStopsRetryAndEscalatesThroughSend(t *testing.T) {
	for _, prior := range []int{0, spawnFailureCap - 1} {
		t.Run(fmt.Sprintf("prior-failures-%d", prior), func(t *testing.T) { goneQueueThreadScenario(t, prior) })
	}
}

func goneQueueThreadScenario(t *testing.T, priorFailures int) {
	t.Helper()
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
	e, ctx, worker, sender := nativeAppEngine(t)
	e.SetHumanNotifier(noContactDesktop{})
	e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{
		binary, "queue", "--thread", "{thread}", "--message", "{message}",
	}}})
	e.wakers.mu.Lock()
	e.wakers.fails = map[string]int{"worker": priorFailures}
	e.wakers.mu.Unlock()
	if _, err := e.query(ctx, func() core.Result {
		l := e.state.Agents["worker"]
		l.Status, l.LastCoordination = core.StatusDormant, time.Now().Add(-contactActivityGrace-time.Minute)
		delete(e.seen, "worker")
		return nil
	}); err != nil {
		t.Fatal("setup:", err)
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
}
