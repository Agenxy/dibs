// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/codexipc"
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/testcodexipc"
)

// The person restarts ChatGPT. Before this, mail arriving while the app came
// back took the cold route and opened each recipient's thread in the person's
// window (dibd.log 2026-10-10 13:01:49-54: two agents, two switches), and
// every later mail to a thread the new app had not loaded did the same.
//
// Through the production doors only: Engine.Run's epoch watcher, a send, the
// real native client against a socket that refuses and then returns. The app
// process observation, its window and the socket are fixtures.
func TestAChatGPTRestartLoadsEveryAgentThreadOnceAndMailNeverOpensOne(t *testing.T) {
	const second = "0199a0b1-c2d3-4e5f-8a9b-0c1d2e3f4a5b"
	t.Setenv("DIBS_DIR", t.TempDir())
	app := testcodexipc.StartRestarting(t, "idle")
	var mu sync.Mutex
	loaded := map[string]bool{}
	var opens []string
	app.Owns = func(thread string) bool { mu.Lock(); defer mu.Unlock(); return loaded[thread] }

	var epoch atomic.Value
	epoch.Store("app:A")
	var samples atomic.Int32
	previousEpoch, previousShower := appRestartEpoch.Load(), shower
	previousOwned, previousReady := appThreadOwned, appReturnReady
	appRestartEpoch.Store(func() (string, bool) { samples.Add(1); return epoch.Load().(string), true })
	appThreadOwned = codexipc.Owned
	appReturnReady = 10 * time.Second
	shower = &harnessenv.Shower{
		Ownership: func(string) harnessenv.ThreadOwnership { return harnessenv.ThreadOwnership{} },
		Open: func(argv []string) error {
			url := argv[len(argv)-1]
			mu.Lock()
			defer mu.Unlock()
			opens = append(opens, url)
			loaded[strings.TrimPrefix(url, "codex://threads/")] = true // navigation loads it
			return nil
		},
		Wait: func(d time.Duration) { <-time.After(d) },
	}
	t.Cleanup(func() {
		appRestartEpoch.Store(previousEpoch)
		shower, appThreadOwned, appReturnReady = previousShower, previousOwned, previousReady
	})

	// A queue command that works, so a cold route taken by mistake would
	// admit the mail and reach the app-open path, where this test sees it.
	binary := filepath.Join(t.TempDir(), "codex")
	if b, err := exec.Command("go", "build", "-o", binary, "../wakeexec/testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("build fixture %v: %s", err, b)
	}
	e, ctx, _, sender := nativeAppEngine(t)
	e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{
		binary, "queue", "--thread", "{thread}", "--message", "{message}",
	}}})
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "another-hand", SessionID: second,
		Agent: &core.AgentInfo{Harness: "Codex", Surface: harnessenv.ChatGPTApp},
	}); err != nil {
		t.Fatal("setup second agent:", err)
	}
	// The watcher settles on the running app (A) before the restart.
	for deadline := time.Now().Add(9 * time.Second); samples.Load() < 3; {
		if time.Now().After(deadline) {
			t.Fatal("setup: the epoch watcher never sampled the app")
		}
		<-time.After(20 * time.Millisecond)
	}

	// Mid-restart: the process is up and the socket refuses.
	epoch.Store("app:B")
	nativeAppSend(t, e, ctx, sender)
	waitWakeDone(t, e, "worker")
	mu.Lock()
	early := append([]string(nil), opens...)
	mu.Unlock()
	if len(early) != 0 || len(app.Inputs()) != 0 {
		t.Fatalf("mail during the restart opened %q or sent %d inputs; it must wait for the app", early, len(app.Inputs()))
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("CODEX_HOME"), "pending.json")); !os.IsNotExist(err) {
		t.Fatal("mail during the restart entered the cold queue:", err)
	}

	app.Return()
	deadline := time.Now().Add(20 * time.Second)
	for len(app.Inputs()) < 1 && time.Now().Before(deadline) {
		<-time.After(20 * time.Millisecond)
	}
	waitWakeDone(t, e, "worker")
	mu.Lock()
	got := append([]string(nil), opens...)
	mu.Unlock()
	want := []string{"codex://threads/" + second, "codex://threads/" + testcodexipc.Thread, "codex://threads/new"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("after the restart the window went to %q, want each agent thread once and then the launch view %q", got, want)
	}
	inputs := app.Inputs()
	if len(inputs) != 1 {
		t.Fatalf("held mail delivered %d times natively, want once", len(inputs))
	}
	p := inputs[0]["params"].(map[string]any)["turnStart"].(map[string]any)["request"].(map[string]any)
	if p["threadId"] != testcodexipc.Thread || p["input"].([]any)[0].(map[string]any)["text"] != "Dibs: new notify from sender." {
		t.Fatalf("held mail reached the wrong thread or carried the wrong notice: %v", p)
	}

	// A later mail goes straight in: nothing opens.
	nativeAppSend(t, e, ctx, sender)
	awaitNative(t, app, 2)
	waitWakeDone(t, e, "worker")
	mu.Lock()
	after := len(opens)
	mu.Unlock()
	if after != len(want) {
		t.Fatalf("mail after the load opened a thread again: %d opens", after)
	}
}

// The gap between the app accepting connections again and Dibs loading its
// threads: the app is up, the thread is not loaded yet, and the cold route
// would open it in the person's window. The load is due, so the mail waits
// for it instead.
func TestMailWaitsForTheLoadWhenTheAppIsBackButItsThreadsAreNot(t *testing.T) {
	t.Setenv("DIBS_DIR", t.TempDir())
	app := testcodexipc.Start(t, "idle", nil)
	app.Owns = func(string) bool { return false }
	previousEpoch, previousShower := appRestartEpoch.Load(), shower
	appRestartEpoch.Store(func() (string, bool) { return "app:B", true })
	var opens atomic.Int32
	shower = &harnessenv.Shower{
		Ownership: func(string) harnessenv.ThreadOwnership { return harnessenv.ThreadOwnership{} },
		Open:      func([]string) error { opens.Add(1); return nil },
	}
	t.Cleanup(func() { appRestartEpoch.Store(previousEpoch); shower = previousShower })
	binary := filepath.Join(t.TempDir(), "codex")
	if b, err := exec.Command("go", "build", "-o", binary, "../wakeexec/testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("build fixture %v: %s", err, b)
	}
	e, ctx, _, sender := nativeAppEngine(t)
	e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{
		binary, "queue", "--thread", "{thread}", "--message", "{message}",
	}}})
	// Dibs last loaded the threads of incarnation A; B is running and its
	// load has not happened. (The watcher's own baseline takes two samples,
	// four seconds, so this send is decided against A.)
	e.appSettled.Store("app:A")
	nativeAppSend(t, e, ctx, sender)
	waitWakeDone(t, e, "worker")
	if n := opens.Load(); n != 0 {
		t.Fatalf("mail opened the thread %d times while the app's load was due", n)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("CODEX_HOME"), "pending.json")); !os.IsNotExist(err) {
		t.Fatal("mail entered the cold queue while the app's load was due:", err)
	}
	if !e.appReturnPending() {
		t.Fatal("setup: the load was not pending, so this proved nothing")
	}
}
