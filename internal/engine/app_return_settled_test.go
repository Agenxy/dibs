// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/codexipc"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/testcodexipc"
)

// Settled is the end of the pass, including a failed/partial pass. If it
// instead means every target was loaded, one missing thread holds future
// unloaded mail forever. The regression enters through a registered app
// agent, the real epoch watcher baseline and load helper, then a REAL send,
// native refusal, queue command and ordinary app-open route. Only the app
// and process observation are fixtures; no test sets appSettled itself.
func TestPartialRestartPassAllowsLaterUnownedMailToOpen(t *testing.T) {
	t.Setenv("DIBS_DIR", t.TempDir())
	app := testcodexipc.Start(t, "idle", nil)
	app.Owns = func(string) bool { return false }
	binary := filepath.Join(t.TempDir(), "codex")
	if b, err := exec.Command("go", "build", "-o", binary, "../wakeexec/testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("build fixture %v: %s", err, b)
	}
	var epoch atomic.Value
	epoch.Store("app:A")
	var samples atomic.Int32
	previousEpoch, previousOwned, previousShower := appRestartEpoch.Load(), appThreadOwned, shower
	appRestartEpoch.Store(func() (string, bool) { samples.Add(1); return epoch.Load().(string), true })
	appThreadOwned = codexipc.Owned
	opened := make(chan struct{}, 1)
	var passOpens atomic.Int32
	shower = &harnessenv.Shower{
		Ownership: func(string) harnessenv.ThreadOwnership {
			return harnessenv.ThreadOwnership{Running: true, Known: true, Loaded: false, Epoch: "app:B"}
		},
		Open: func([]string) error {
			if _, err := os.Stat(filepath.Join(os.Getenv("CODEX_HOME"), "pending.json")); err != nil {
				if !os.IsNotExist(err) {
					return err
				}
				passOpens.Add(1)
				return errors.New("fixture: restart navigation refused")
			}
			select {
			case opened <- struct{}{}:
			default:
			}
			return nil
		},
	}
	t.Cleanup(func() {
		appRestartEpoch.Store(previousEpoch)
		appThreadOwned, shower = previousOwned, previousShower
	})
	e, ctx, _, sender := nativeAppEngine(t)
	e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{binary, "queue", "--thread", "{thread}", "--message", "{message}"}}})
	for deadline := time.Now().Add(9 * time.Second); samples.Load() < 3; {
		if time.Now().After(deadline) {
			t.Fatal("setup: epoch watcher never sampled the app")
		}
		<-time.After(20 * time.Millisecond)
	}
	if settled, _ := e.appSettled.Load().(string); settled != "app:A" {
		t.Fatalf("setup: watcher baseline=%q", settled)
	}
	epoch.Store("app:B")
	e.afterAppReturn(ctx, "app:B", nil, 0)
	if passOpens.Load() != 1 {
		t.Fatalf("setup: partial load did not attempt one refused navigation: %d", passOpens.Load())
	}
	nativeAppSend(t, e, ctx, sender)
	select {
	case <-opened:
	case <-time.After(5 * time.Second):
		t.Fatalf("later unloaded mail did not reach its ordinary open route; appReturnPending=%t", e.appReturnPending())
	}
	waitWakeDone(t, e, "worker")
	if e.appReturnPending() {
		t.Fatal("finished partial pass still holds later mail")
	}
	if len(app.Inputs()) != 0 {
		t.Fatal("unowned fixture unexpectedly accepted native input")
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("CODEX_HOME"), "pending.json")); err != nil {
		t.Fatal("ordinary route did not admit the queued notice:", err)
	}
}
