// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/testcodexipc"
	"github.com/agenxy/dibs/internal/testexec"
)

func TestDeferredQueueWakeReconsidersOriginalMailOnNextSend(t *testing.T) {
	t.Setenv("DIBS_DIR", t.TempDir())
	testcodexipc.Start(t, "no-owner", nil)
	binary := filepath.Join(t.TempDir(), "codex")
	if out, err := exec.Command("go", "build", "-o", binary, "../wakeexec/testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("setup: build queue fixture: %v: %s", err, out)
	}
	testexec.PrepareExecutable(t, exec.Command(binary, "--version"))
	previous := shower
	shower = &harnessenv.Shower{
		Holds: func(string) bool { return true },
		Open:  func([]string) error { t.Error("test queue opened the app"); return nil },
	}
	t.Cleanup(func() { shower = previous })
	e, ctx, _, sender := nativeAppEngine(t)
	e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{
		binary, "queue", "--thread", "{thread}", "--message", "{message}",
	}}})
	send := func() uint64 {
		t.Helper()
		serial := nativeAppSend(t, e, ctx, sender)
		waitWakeDone(t, e, "worker")
		return serial
	}
	queuePath := filepath.Join(os.Getenv("CODEX_HOME"), "pending.json")
	check := func(serial uint64, wantedAdmission string, presented bool) {
		t.Helper()
		if _, err := e.query(ctx, func() core.Result {
			e.wakers.mu.Lock()
			observation := e.wakers.queueObserved["worker"].observation
			timer, failures := e.wakers.deferred["worker"], e.wakers.fails["worker"]
			e.wakers.mu.Unlock()
			m := e.state.Messages[serial]
			offered := e.commandWritten["mail:"+noticeKey("worker", serial)]
			if observation.Admission != wantedAdmission || offered != presented || m == nil ||
				m.Consumed || e.notifyPresented("worker", m) != presented || timer != nil || failures != 0 {
				t.Errorf("message %d: admission=%q offered=%t mail=%+v has_retry=%t failures=%d, want %q offered=%t",
					serial, observation.Admission, offered, m, timer != nil, failures, wantedAdmission, presented)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	turnEnded := func() time.Time {
		t.Helper()
		got, err := e.query(ctx, func() core.Result { return core.Result{"ended": e.turnEnded["worker"]} })
		if err != nil {
			t.Fatal(err)
		}
		return got["ended"].(time.Time)
	}
	check(send(), "accepted", true)
	before := turnEnded()
	if err := os.WriteFile(queuePath, []byte("[]"), 0o600); err != nil {
		t.Fatal("setup: consume first queue item:", err)
	}
	t.Setenv("DIBS_TEST_QUEUE_LIST_UNKNOWN", "1")
	deferred := send()
	check(deferred, "deferred", false)
	if got := turnEnded(); !got.Equal(before) {
		t.Fatal("receipt-only deferral recorded a turn end")
	}
	if raw, err := os.ReadFile(queuePath); err != nil || string(raw) != "[]" {
		t.Fatalf("receipt-only deferral enqueued input: %q %v", raw, err)
	}
	t.Setenv("DIBS_TEST_QUEUE_LIST_UNKNOWN", "")
	check(send(), "accepted", true)
	check(deferred, "accepted", true)
}
