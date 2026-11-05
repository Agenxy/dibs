// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/testcodexipc"
	"github.com/agenxy/dibs/internal/testexec"
	"github.com/agenxy/dibs/internal/wakeexec"
)

func TestAmbiguousCommandDoesNotArmEngineRetry(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "codex")
	if out, err := exec.Command("go", "build", "-o", binary, "../wakeexec/testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("setup fixture build: %v: %s", err, out)
	}
	testexec.PrepareExecutable(t, exec.Command(binary, "--version"))
	for _, entry := range []string{"send", "deferred"} {
		for _, mode := range []string{"queue-unconfirmed", "nonqueue-unconfirmed", "definite-failure"} {
			t.Run(entry+"/"+mode, func(t *testing.T) { commandRetryScenario(t, binary, mode, entry == "deferred") })
		}
	}
}

func commandRetryScenario(t *testing.T, binary, mode string, deferred bool) {
	t.Helper()
	t.Setenv("DIBS_DIR", t.TempDir())
	testcodexipc.Start(t, "no-owner", nil)
	home := os.Getenv("CODEX_HOME")
	t.Setenv("DIBS_TEST_QUEUE_EVIDENCE", home)
	previousShower := shower
	shower = &harnessenv.Shower{Holds: func(string) bool { return true }, Open: func([]string) error {
		t.Error("uncertain or failed command opened the app")
		return nil
	}}
	t.Cleanup(func() { shower = previousShower })
	logs := commandOutcomeLog{finished: make(chan struct{})}
	previousLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLog) })
	e, ctx, _, sender := nativeAppEngine(t)
	e.SetHumanNotifier(noContactDesktop{})
	argv := []string{binary, "queue", "--thread", "{thread}", "--message", "{message}"}
	commandMode := "queue"
	switch mode {
	case "nonqueue-unconfirmed":
		argv, commandMode = []string{binary, "nonqueue-pipe"}, "nonqueue-pipe"
	case "definite-failure":
		argv, commandMode = []string{binary, "primary"}, "primary"
	}
	// Keep any incorrectly armed timer observable rather than racing its fire.
	// The production armer and retry decision still run; cleanup stops timers.
	e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: argv, Cooldown: time.Hour}})
	if _, err := e.query(ctx, func() core.Result {
		l := e.state.Agents["worker"]
		l.Status, l.LastCoordination = core.StatusDormant, time.Now().Add(-contactActivityGrace-time.Minute)
		delete(e.seen, "worker")
		return nil
	}); err != nil {
		t.Fatal("setup dormant worker:", err)
	}
	send := func() uint64 {
		if deferred {
			e.SetWakePolicy(WakeNone)
		}
		serial := commandRetrySend(t, e, ctx, sender)
		if deferred {
			e.SetWakePolicy(WakeAll)
			// Exercise the retry-entry goroutine with its first actual command,
			// not a hand-set no-retry flag or a hand-called outcome reader.
			if _, err := e.query(ctx, func() core.Result { e.retryWakeDecision("worker"); return nil }); err != nil {
				t.Fatal("setup deferred wake:", err)
			}
		}
		return serial
	}
	controls := map[byte]net.Conn{}
	if mode != "definite-failure" {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal("setup pipe control:", err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		if err := ln.(*net.TCPListener).SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		t.Setenv("DIBS_TEST_QUEUE_PIPE_CONTROL", ln.Addr().String())
		t.Setenv("DIBS_TEST_QUEUE_PIPE_CONTROL_TIMEOUT", "30s")
		// The queue item really commits and disappears before its receipt probe.
		t.Setenv("DIBS_TEST_QUEUE_PIPE_CASE", "dispatched")
		t.Cleanup(func() {
			for role, control := range controls {
				_ = control.SetDeadline(time.Now().Add(5 * time.Second))
				release := byte('x')
				if role == 'p' {
					release = 'e'
				}
				_, _ = control.Write([]byte{release})
				if extra, err := io.ReadAll(control); err != nil || len(extra) != 0 {
					t.Errorf("fixture %c exit receipt: %q %v", role, extra, err)
				}
				_ = control.Close()
			}
		})
		// Send is the production entrance: maybeWake, runWakePlan, failure
		// reporting, and the failed-wake armer all execute without a test setter.
		serial := send()
		for range 2 {
			control, err := ln.Accept()
			if err != nil {
				t.Fatal("setup command did not reach its real pipe:", err)
			}
			_ = control.SetReadDeadline(time.Now().Add(5 * time.Second))
			var role [1]byte
			if _, err := io.ReadFull(control, role[:]); err != nil || (role[0] != 'p' && role[0] != 'g') || controls[role[0]] != nil {
				_ = control.Close()
				t.Fatalf("setup fixture role: %q %v", role, err)
			}
			controls[role[0]] = control
		}
		if _, err := controls['p'].Write([]byte{'e'}); err != nil {
			t.Fatal("setup parent release:", err)
		}
		waitCommandRetryDone(t, e, logs.finished)
		if !strings.Contains(logs.String(), "ErrWaitDelay") {
			t.Fatalf("setup did not reach ambiguous output drain: %s", logs.String())
		}
		assertCommandRetryState(t, e, serial, false)
		if mode == "queue-unconfirmed" {
			data, err := os.ReadFile(filepath.Join(home, "dispatched.json"))
			if err != nil || !bytes.Contains(data, []byte(testcodexipc.Thread)) {
				t.Fatalf("setup did not commit and dispatch the actual queue item: %q %v", data, err)
			}
		}
	} else {
		serial := send()
		waitCommandRetryDone(t, e, logs.finished)
		assertCommandRetryState(t, e, serial, true)
	}
	data, err := os.ReadFile(filepath.Join(home, "stages.jsonl"))
	if err != nil {
		t.Fatal("setup command evidence:", err)
	}
	entered, finished := 0, 0
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		var event struct{ Mode, Phase string }
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal("setup command evidence:", err)
		}
		if event.Mode == commandMode {
			if event.Phase == "entered" {
				entered++
			}
			if event.Phase == "finished" {
				finished++
			}
		}
	}
	if entered != 1 || (mode != "definite-failure" && finished != 1) {
		t.Fatalf("command did not execute exactly once and exit: entered=%d finished=%d", entered, finished)
	}
}

func commandRetrySend(t *testing.T, e *Engine, ctx context.Context, sender string) uint64 {
	t.Helper()
	r, err := e.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: sender, To: "worker", MsgType: core.MsgRequest, Body: "unread retry fixture"})
	if err != nil {
		t.Fatal("setup send:", err)
	}
	serial, ok := r["msg_serial"].(uint64)
	if !ok {
		t.Fatalf("setup send did not apply: %v", r)
	}
	return serial
}

// The real adapter emits either ErrWaitDelay or definite command failure.
// Observing that event before waiting for engine bookkeeping prevents a send
// that has not started from looking like a completed command with no retry.
type commandOutcomeLog struct {
	economyLog
	finished chan struct{}
	once     sync.Once
}

func (l *commandOutcomeLog) Write(p []byte) (int, error) {
	n, err := l.economyLog.Write(p)
	if bytes.Contains(p, []byte("ErrWaitDelay")) || bytes.Contains(p, []byte("wake command failed")) {
		l.once.Do(func() { close(l.finished) })
	}
	return n, err
}

func waitCommandRetryDone(t *testing.T, e *Engine, finished <-chan struct{}) {
	t.Helper()
	select {
	case <-finished:
		waitWakeDone(t, e, "worker")
	case <-time.After(wakeexec.Grace + 10*time.Second):
		t.Fatal("command did not finish after the production output-drain grace")
	}
}

func assertCommandRetryState(t *testing.T, e *Engine, serial uint64, wantRetry bool) {
	t.Helper()
	e.wakers.mu.Lock()
	armed := e.wakers.deferred["worker"] != nil
	failed := e.wakers.failedCauses["worker"]
	e.wakers.mu.Unlock()
	if armed != wantRetry || (len(failed.keys) > 0) != wantRetry {
		t.Errorf("engine retry armed=%v cause=%v, want retry=%v", armed, failed.keys, wantRetry)
	}
	r, err := e.query(context.Background(), func() core.Result {
		m := e.state.Messages[serial]
		return core.Result{"unread": m != nil && m.State == core.MsgStatePending && !m.Consumed && m.DeliveredAt == 0}
	})
	if err != nil || r["unread"] != true {
		t.Fatalf("setup mail did not remain unread: %v %v", r, err)
	}
}
