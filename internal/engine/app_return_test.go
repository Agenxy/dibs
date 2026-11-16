// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"encoding/json"
	"fmt"
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
	"github.com/agenxy/dibs/internal/paths"
	"github.com/agenxy/dibs/internal/testcodexipc"
	"github.com/agenxy/dibs/internal/testexec"
)

// The person restarts ChatGPT. Before this, mail arriving while the app came
// back took the cold route and opened each recipient's thread in the person's
// window (dibd.log 2026-10-10 13:01:49-54: two agents, two switches), and
// every later mail to a thread the new app had not loaded did the same.
//
// Through the production doors only: Engine.Run's epoch watcher, a send, the
// real native client against a socket that refuses and then returns. The app
// process observation, its window and the socket are fixtures.
func TestAppReturnQueuesBeforeOneWakeAndOneTurnPerOwedMessage(t *testing.T) {
	const second = "0199a0b1-c2d3-4e5f-8a9b-0c1d2e3f4a5b"
	t.Setenv("DIBS_DIR", t.TempDir())
	app := testcodexipc.StartRestarting(t, "idle")
	var mu sync.Mutex
	loaded := map[string]bool{}
	var opens []string
	var queued []returnQueueTurn
	app.Owns = func(thread string) bool { mu.Lock(); defer mu.Unlock(); return loaded[thread] }
	var epoch atomic.Value
	epoch.Store("app:A")
	var samples atomic.Int32
	previousEpoch, previousShower := appRestartEpoch.Load(), shower
	previousOwned, previousReady := appThreadOwned, appReturnReady
	appRestartEpoch.Store(func() (string, bool) { samples.Add(1); return epoch.Load().(string), true })
	appThreadOwned, appReturnReady = codexipc.Owned, 10*time.Second
	shower = &harnessenv.Shower{
		Ownership: func(string) harnessenv.ThreadOwnership { return harnessenv.ThreadOwnership{} },
		Open: func(argv []string) error {
			const prefix = "codex://threads/wake/"
			if len(argv) != 3 || argv[0] != "/usr/bin/open" || argv[1] != "-g" || !strings.HasPrefix(argv[2], prefix) {
				return fmt.Errorf("unexpected background wake: %q", argv)
			}
			thread := strings.TrimPrefix(argv[2], prefix)
			turns, err := consumeReturnQueue(thread)
			mu.Lock()
			defer mu.Unlock()
			opens = append(opens, argv[2])
			queued = append(queued, turns...)
			// Match the measured handler: an empty queue never loads a thread.
			if len(turns) > 0 {
				loaded[thread] = true
			}
			return err
		},
		Wait: func(d time.Duration) { <-time.After(d) },
	}
	t.Cleanup(func() {
		appRestartEpoch.Store(previousEpoch)
		shower, appThreadOwned, appReturnReady = previousShower, previousOwned, previousReady
	})
	binary := filepath.Join(t.TempDir(), "codex")
	if b, err := exec.Command("go", "build", "-o", binary, "../wakeexec/testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("build fixture %v: %s", err, b)
	}
	testexec.PrepareExecutable(t, exec.Command(binary, "--version"))
	e, ctx, _, sender := nativeAppEngine(t)
	e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{binary, "queue", "--thread", "{thread}", "--message", "{message}"}}})
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "another-hand", SessionID: second,
		Agent: &core.AgentInfo{Harness: "Codex", Surface: harnessenv.ChatGPTApp},
	}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(9 * time.Second); samples.Load() < 3; {
		if time.Now().After(deadline) {
			t.Fatal("setup: epoch watcher did not sample the app")
		}
		<-time.After(20 * time.Millisecond)
	}
	epoch.Store("app:B")
	nativeAppSend(t, e, ctx, sender)
	waitWakeDone(t, e, "worker")
	mu.Lock()
	early := len(opens)
	mu.Unlock()
	if early != 0 || len(app.Inputs()) != 0 {
		t.Fatal("mail reached the app before it returned")
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("CODEX_HOME"), "pending.json")); !os.IsNotExist(err) {
		t.Fatal("mail entered the cold queue while the app was unavailable:", err)
	}
	app.Return()
	waitQueued := func(want int) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for {
			mu.Lock()
			got := len(queued)
			mu.Unlock()
			if got >= want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("queued turns=%d, want %d", got, want)
			}
			<-time.After(20 * time.Millisecond)
		}
	}
	waitQueued(1)
	waitWakeDone(t, e, "worker")
	mu.Lock()
	firstOpens := append([]string(nil), opens...)
	firstTurns := append([]returnQueueTurn(nil), queued...)
	mu.Unlock()
	if len(firstOpens) != 1 || firstOpens[0] != "codex://threads/wake/"+testcodexipc.Thread || len(firstTurns) != 1 || len(app.Inputs()) != 0 {
		t.Fatalf("return must cause one admitted wake and one turn: urls=%q queued=%v native=%d", firstOpens, firstTurns, len(app.Inputs()))
	}
	if firstTurns[0].Thread != testcodexipc.Thread {
		t.Fatalf("wrong queue recipient: %v", firstTurns)
	}
	text := firstTurns[0].Text
	const prefix, suffix = "Dibs: notify notice issued at ", ". It may already be handled."
	stamp := strings.TrimSuffix(strings.TrimPrefix(text, prefix), suffix)
	if _, err := time.Parse(time.RFC3339, stamp); err != nil || !strings.HasPrefix(text, prefix) || !strings.HasSuffix(text, suffix) {
		t.Fatalf("cold input changed its factual, body-free notice: %q", text)
	}
	nativeAppSend(t, e, ctx, sender)
	awaitNative(t, app, 1)
	waitWakeDone(t, e, "worker")
	native := app.Inputs()
	if len(native) != 1 {
		t.Fatalf("owned follow-up delivered %d native inputs, want one", len(native))
	}
	input := native[0]["params"].(map[string]any)["turnStart"].(map[string]any)["request"].(map[string]any)
	if input["threadId"] != testcodexipc.Thread || input["input"].([]any)[0].(map[string]any)["text"] != "Dibs: new notify from sender." {
		t.Fatalf("wrong owned input: %v", input)
	}
	mu.Lock()
	after := len(opens)
	mu.Unlock()
	if after != 1 {
		t.Fatalf("owned follow-up added a wake URL: %d", after)
	}
	if r, err := e.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: sender, To: "another-hand", MsgType: core.MsgNotify, Body: "new quiet-agent mail"}); err != nil || r["msg_serial"] == nil {
		t.Fatalf("quiet send: %v %v", r, err)
	}
	waitQueued(2)
	waitWakeDone(t, e, "another-hand")
	mu.Lock()
	finalOpens := append([]string(nil), opens...)
	finalTurns := append([]returnQueueTurn(nil), queued...)
	mu.Unlock()
	if len(finalOpens) != 2 || finalOpens[1] != "codex://threads/wake/"+second || len(finalTurns) != 2 || finalTurns[1].Thread != second || len(app.Inputs()) != 1 {
		t.Fatalf("quiet message must add exactly one wake and turn: urls=%q queued=%v native=%d", finalOpens, finalTurns, len(app.Inputs()))
	}
}

type returnQueueTurn struct{ Thread, Text string }

// The fake app consumes only actually admitted queue entries, under the same
// fixture transaction lock as the real queue writer. No pending input is a no-op.
func consumeReturnQueue(thread string) ([]returnQueueTurn, error) {
	path := filepath.Join(os.Getenv("CODEX_HOME"), "pending.json")
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Close() }()
	if err = paths.LockExclusive(lock, true); err != nil {
		return nil, err
	}
	defer paths.Unlock(lock)
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	if err = json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	keep := make([]map[string]any, 0, len(rows))
	var turns []returnQueueTurn
	for _, row := range rows {
		if row["threadId"] != thread {
			keep = append(keep, row)
			continue
		}
		input, ok := row["input"].([]any)
		if !ok || len(input) != 1 {
			return nil, fmt.Errorf("invalid queued input: %v", row)
		}
		text, ok := input[0].(map[string]any)
		if !ok || text["type"] != "text" {
			return nil, fmt.Errorf("invalid queue text: %v", input)
		}
		value, ok := text["text"].(string)
		if !ok {
			return nil, fmt.Errorf("queue text is not a string")
		}
		turns = append(turns, returnQueueTurn{thread, value})
	}
	raw, err = json.Marshal(keep)
	if err != nil {
		return nil, err
	}
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		return nil, err
	}
	return turns, nil
}

// The gap between the app accepting connections again and the return handshake: the app is up, the thread is not loaded yet, and the cold route
// would open it in the person's window. The handshake is due, so the mail waits
// for it instead.
func TestMailWaitsForTheAppReturnHandshake(t *testing.T) {
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
	testexec.PrepareExecutable(t, exec.Command(binary, "--version"))
	e, ctx, _, sender := nativeAppEngine(t)
	e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{
		binary, "queue", "--thread", "{thread}", "--message", "{message}",
	}}})
	// Dibs last loaded the threads of incarnation A; B is running and its
	// return handshake has not happened. (The watcher's own baseline takes two samples,
	// four seconds, so this send is decided against A.)
	e.appSettled.Store("app:A")
	nativeAppSend(t, e, ctx, sender)
	waitWakeDone(t, e, "worker")
	if n := opens.Load(); n != 0 {
		t.Fatalf("mail opened the thread %d times while the return handshake was due", n)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("CODEX_HOME"), "pending.json")); !os.IsNotExist(err) {
		t.Fatal("mail entered the cold queue while the return handshake was due:", err)
	}
	if !e.appReturnPending() {
		t.Fatal("setup: the return handshake was not pending, so this proved nothing")
	}
}
