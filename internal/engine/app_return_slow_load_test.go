// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/agenxy/dibs/internal/codexipc"
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/testcodexipc"
	"github.com/agenxy/dibs/internal/testexec"
)

func TestExistingUnownedThreadRemainsOpenableAfterRestartSettle(t *testing.T) {
	t.Setenv("DIBS_DIR", t.TempDir())
	app := testcodexipc.Start(t, "idle", nil)
	// A real local rollout distinguishes a slow/closed owner from a missing chat.
	sessions := filepath.Join(os.Getenv("CODEX_HOME"), "sessions")
	if err := os.Mkdir(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	header := `{"type":"session_meta","payload":{"id":"` + testcodexipc.Thread + `","originator":"Codex Desktop"}}`
	if err := os.WriteFile(filepath.Join(sessions, "rollout-fixture-"+testcodexipc.Thread+".jsonl"), []byte(header), 0o600); err != nil {
		t.Fatal(err)
	}
	if !harnessenv.CodexBornInApp(testcodexipc.Thread) {
		t.Fatal("setup: existing app thread not established")
	}
	var owned atomic.Bool
	app.Owns = func(string) bool { return owned.Load() }
	var opens atomic.Int32
	previousEpoch, previousOwned, previousShower := appRestartEpoch.Load(), appThreadOwned, shower
	appRestartEpoch.Store(func() (string, bool) { return "app:B", true })
	appThreadOwned = codexipc.Owned
	e, ctx, worker, sender := nativeAppEngine(t)
	var original uint64
	shower = &harnessenv.Shower{Ownership: func(string) harnessenv.ThreadOwnership {
		return harnessenv.ThreadOwnership{Running: true, Known: true, Loaded: owned.Load(), Epoch: "app:B"}
	}, Open: func(argv []string) error {
		if argv[len(argv)-1] != "codex://threads/new" {
			opens.Add(1)
		} else if original != 0 {
			// The initial mail is read through Dibs while the owner is still absent.
			// This isolates the next real mail from the existing original-mail retry.
			r, err := e.Do(ctx, &core.Op{Kind: core.OpAckMessage, Token: worker, MsgSerial: original})
			if err != nil {
				return err
			}
			if r["error"] != nil {
				t.Fatalf("setup ack failed: %v", r)
			}
			original = 0
		}
		return nil
	}}
	t.Cleanup(func() { appRestartEpoch.Store(previousEpoch); appThreadOwned, shower = previousOwned, previousShower })
	binary := filepath.Join(t.TempDir(), "codex")
	if b, err := exec.Command("go", "build", "-o", binary, "../wakeexec/testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("fixture build: %v %s", err, b)
	}
	testexec.PrepareExecutable(t, exec.Command(binary, "--version"))
	e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{binary, "queue", "--thread", "{thread}", "--message", "{message}"}}})
	e.appSettled.Store("app:A")
	original = nativeAppSend(t, e, ctx, sender)
	waitWakeDone(t, e, "worker")
	e.afterAppReturn(ctx, "app:B", nil, 0)
	waitWakeDone(t, e, "worker")
	if opens.Load() != 1 {
		t.Fatalf("unowned restart binding navigated %d times, want one attempt", opens.Load())
	}
	nativeAppSend(t, e, ctx, sender)
	waitWakeDone(t, e, "worker")
	e.afterAppReturn(ctx, "app:B", nil, 0)
	waitWakeDone(t, e, "worker")
	if opens.Load() < 2 {
		t.Fatalf("existing thread became permanently unopenable after settle: %d opens", opens.Load())
	}
	board, err := e.Board(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if board["app_binding_alerts"] != nil {
		t.Fatal("existing unowned thread was diagnosed as missing")
	}
	beforeNative := opens.Load()
	// A load finishing later admits native input without another navigation.
	owned.Store(true)
	nativeAppSend(t, e, ctx, sender)
	awaitNative(t, app, 1)
	waitWakeDone(t, e, "worker")
	if opens.Load() != beforeNative {
		t.Fatal("late native recovery navigated")
	}
	board, err = e.Board(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if board["app_binding_alerts"] != nil {
		t.Fatal("confirmed native binding kept a stale diagnostic")
	}
}
