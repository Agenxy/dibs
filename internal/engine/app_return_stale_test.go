// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/agenxy/dibs/internal/codexipc"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/testcodexipc"
	"github.com/agenxy/dibs/internal/testexec"
)

func TestUnownedAfterRestartOpenIsQuarantinedAcrossLaterMailAndRestart(t *testing.T) {
	t.Setenv("DIBS_DIR", t.TempDir())
	app := testcodexipc.Start(t, "idle", nil)
	var owned atomic.Bool
	app.Owns = func(string) bool { return owned.Load() }
	var opens atomic.Int32
	previousEpoch, previousOwned, previousShower := appRestartEpoch.Load(), appThreadOwned, shower
	appRestartEpoch.Store(func() (string, bool) { return "app:B", true })
	appThreadOwned = codexipc.Owned
	shower = &harnessenv.Shower{Ownership: func(string) harnessenv.ThreadOwnership {
		return harnessenv.ThreadOwnership{Running: true, Known: true, Loaded: owned.Load(), Epoch: "app:B"}
	}, Open: func(argv []string) error {
		if argv[len(argv)-1] != "codex://threads/new" {
			opens.Add(1)
		}
		return nil
	}}
	t.Cleanup(func() { appRestartEpoch.Store(previousEpoch); appThreadOwned, shower = previousOwned, previousShower })
	binary := filepath.Join(t.TempDir(), "codex")
	if b, err := exec.Command("go", "build", "-o", binary, "../wakeexec/testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("fixture build: %v %s", err, b)
	}
	testexec.PrepareExecutable(t, exec.Command(binary, "--version"))
	e, ctx, _, sender := nativeAppEngine(t)
	e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{binary, "queue", "--thread", "{thread}", "--message", "{message}"}}})
	e.appSettled.Store("app:A")
	nativeAppSend(t, e, ctx, sender)
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
	if opens.Load() != 1 {
		t.Fatalf("later mail/restart reopened stale binding %d times", opens.Load())
	}
	board, err := e.Board(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if board["app_binding_alerts"] == nil {
		t.Fatal("stale binding was not surfaced on the board")
	}
	// Observed ownership allows native input again, without another navigation.
	owned.Store(true)
	nativeAppSend(t, e, ctx, sender)
	awaitNative(t, app, 1)
	waitWakeDone(t, e, "worker")
	if opens.Load() != 1 {
		t.Fatal("native recovery navigated")
	}
	board, err = e.Board(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if board["app_binding_alerts"] != nil {
		t.Fatal("confirmed native binding kept a stale diagnostic")
	}
}
