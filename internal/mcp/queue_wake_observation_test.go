// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/paths"
	"github.com/agenxy/dibs/internal/testcodexipc"
	"github.com/agenxy/dibs/internal/testexec"
)

// Real MCP send -> writer planner -> native command/probe -> board and send
// receipts. Only the app contact and subprocess harness are fixtures. The same
// test compiles on the parent commit: it cannot seed the new observation cache.
func TestQueueWakeObservationsThroughMCPAndActualCommand(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "codex")
	if b, err := exec.Command("go", "build", "-o", binary, "../wakeexec/testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("setup: fixture build: %v: %s", err, b)
	}
	testexec.PrepareExecutable(t, exec.Command(binary, "--version"))
	for _, mode := range []string{
		"admitted", "not-listening", "native-failed", "thread-unloaded",
		"pending-aged", "probe-unknown", "legacy-age", "fallback", "rebound",
	} {
		t.Run(mode, func(t *testing.T) { queueWakeObservationCase(t, binary, mode) })
	}
}

func queueWakeObservationCase(t *testing.T, binary, mode string) {
	t.Helper()
	const thread = "0199a0b1-c2d3-4e5f-8a9b-0c1d2e3f4a5b"
	home := t.TempDir()
	switch mode {
	case "not-listening":
		testcodexipc.StartRestarting(t, "idle")
		home = os.Getenv("CODEX_HOME")
	case "native-failed":
		testcodexipc.Start(t, "changed", nil)
		home = os.Getenv("CODEX_HOME")
	case "thread-unloaded":
		testcodexipc.Start(t, "no-owner", nil)
		home = os.Getenv("CODEX_HOME")
	}
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", home)
	dir := t.TempDir()
	t.Setenv("DIBS_DIR", dir)
	// A loaded thread, including a busy one, must never be force-opened. The
	// read-only queue interface says nothing about its busy/idle state.
	previous := harnessenv.RealShower
	var opens atomic.Int64
	contacts := make(chan struct{}, 32)
	harnessenv.RealShower = harnessenv.Shower{
		Holds: func(string) bool { contacts <- struct{}{}; return true },
		Open:  func([]string) error { opens.Add(1); return nil },
	}
	t.Cleanup(func() { harnessenv.RealShower = previous })
	srv, eng, _ := newServerWithEngine(t)
	argv := []string{binary, "queue", "--thread", "{thread}", "--message", "{message}"}
	cmd := engine.WakeCommand{Argv: argv, Cooldown: time.Millisecond}
	if mode == "fallback" {
		cmd.Argv, cmd.Fallback = []string{binary, "primary"}, argv
	}
	eng.SetWakeCommands(map[string]engine.WakeCommand{"codex": cmd})
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		out := toolCall(t, srv, name, args)
		if out["code"] != nil || out["__is_error"] == true {
			t.Fatalf("setup: %s: %v", name, out)
		}
		return out
	}
	worker := registerObservationAppWorker(t, srv, eng, thread, home)
	sender := call("register", map[string]any{"name": "queue-sender", "nonce": "queue-observation-sender-nonce"})
	token := sender["token"]
	call("check_in", map[string]any{"token": token})
	send := func() map[string]any {
		t.Helper()
		requireDormantSendFixture(t, eng, "queue-worker")
		return call("send", map[string]any{"token": token, "to": "queue-worker", "type": "question", "body": "private observation fixture"})
	}
	if mode == "pending-aged" || mode == "legacy-age" {
		text := "Dibs: a new question is waiting."
		if mode == "pending-aged" {
			text = "Dibs: question notice issued at " + time.Now().Add(-13*time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339) + ". It may already be handled."
		}
		writeObservationQueue(t, home, thread, text)
	}
	initial := send()
	note, _ := initial["note"].(string)
	if (!strings.Contains(note, "App queue admission") && !strings.Contains(note, "Direct delivery in progress")) ||
		strings.Contains(note, "when it next wakes") {
		t.Fatalf("dormant Codex send did not describe its app route: %q", note)
	}
	// Confirm the real subprocess door ran before checking a new field. On old
	// code the setup remains green, and only the missing visibility is RED.
	awaitObservationQueueCommand(t, home)
	awaitQueueAppContact(t, contacts, dir, thread)
	wantAdmission, wantPending := "accepted", "unknown"
	if mode == "pending-aged" || mode == "legacy-age" {
		wantAdmission, wantPending = "retained", "pending"
	}
	view := assertQueueWakeView(t, srv, token, wantAdmission, wantPending)
	if mode == "probe-unknown" {
		t.Setenv("DIBS_QUEUE_PROBE_FAIL", "1")
		send()
		awaitQueueWakeObservationAfter(t, eng, view["observed_at"])
		view = assertQueueWakeView(t, srv, token, "deferred", "unknown")
		if view["age_source"] != "unknown" || view["wake_age_seconds"] != nil || view["receipt_age_seconds"] == nil {
			t.Fatalf("unavailable probe lost its receipt age or invented a notice age: %v", view)
		}
	}
	if mode == "pending-aged" && (view["age_source"] != "notice" || view["wake_age_seconds"].(float64) < 12*3600) {
		t.Fatalf("pending notice was redated by coalescing: %v", view)
	}
	if mode == "legacy-age" && (view["issued_at"] != nil || view["wake_age_seconds"] != nil || view["age_source"] != "unknown") {
		t.Fatalf("legacy notice acquired an invented age: %v", view)
	}
	if view["thread_state"] != "unknown" || view["observed_at"] == nil || view["observation_age_seconds"] == nil {
		t.Fatalf("queue observation became a thread-state claim or lost its age: %v", view)
	}
	// The actual send advisory path must carry the same derived facts. This
	// assertion cannot be satisfied by wiring only an unused exported wrapper.
	receipt := send()
	if mode == "pending-aged" || mode == "probe-unknown" {
		// Coalescing in its recovery window and receipt-only deferral publish
		// a newer observation without another app contact.
		awaitQueueWakeObservationAfter(t, eng, view["observed_at"])
	} else {
		awaitQueueAppContact(t, contacts, dir, thread)
	}
	received, ok := receipt["queue_wake"].(map[string]any)
	if !ok || received["thread_state"] != "unknown" || received["observed_at"] == nil {
		t.Fatalf("send receipt lost the adapter observation: %v", receipt)
	}
	if want := map[string]string{
		"admitted": "queue_only", "not-listening": "queue_only",
		"native-failed": "queued_native_failed", "thread-unloaded": "queued_unloaded",
	}[mode]; want != "" {
		if board, receipt := view["native_delivery"], received["native_delivery"]; board != want || receipt != want {
			t.Errorf("accepted queue wake reported board %v and receipt %v, want %q", board, receipt, want)
		}
		if want == "queue_only" {
			note, _ := receipt["note"].(string)
			if !strings.Contains(note, "Last wake queued in the ChatGPT app.") ||
				!strings.Contains(note, "Queue admission does not confirm a started turn or read mail.") {
				t.Errorf("queue-only receipt hid admission or overstated delivery: %q", note)
			}
		}
	}
	if mode == "probe-unknown" {
		if received["admission"] != "deferred" || received["pending"] != "unknown" || received["receipt_age_seconds"] == nil {
			t.Fatalf("send receipt lost its prior deferral or receipt age: %v", received)
		}
		select {
		case <-contacts:
			t.Fatal("receipt-only deferral contacted the app")
		default:
		}
	}
	b, err := json.Marshal(received)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), thread) || strings.Contains(string(b), "private observation fixture") {
		t.Fatalf("receipt exposed a private thread or mail body: %s", b)
	}
	if mode == "rebound" {
		changed := toolCallWithMeta(t, srv, "update", map[string]any{
			"token": worker["token"], "description": "moved session",
		}, map[string]any{"threadId": "0199a0b1-c2d3-4e5f-8a9b-0c1d2e3f4a5c"})
		if changed["code"] != nil {
			t.Fatalf("setup: rebind: %v", changed)
		}
		view = assertQueueWakeView(t, srv, token, "unconfirmed", "unknown")
		if view["issued_at"] != nil || view["observed_at"] != nil {
			t.Fatalf("new session inherited another session's receipt: %v", view)
		}
	}
	if opens.Load() != 0 {
		t.Fatalf("queue status opened a loaded/busy thread %d times", opens.Load())
	}
	items, err := os.ReadFile(filepath.Join(home, "pending.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []any
	if err = json.Unmarshal(items, &rows); err != nil || len(rows) != 1 {
		t.Fatalf("existing coalescer changed: %d pending items: %v", len(rows), err)
	}
}

func registerObservationAppWorker(t *testing.T, srv *httptest.Server, eng *engine.Engine, thread, home string) map[string]any {
	t.Helper()
	worker := toolCallWithMeta(t, srv, "register", map[string]any{
		"name": "queue-worker", "nonce": "queue-observation-worker-nonce",
		"session_id": thread, "harness": "Codex", "cwd": home, "kind": "persistent",
	}, map[string]any{SurfaceMetaKey: harnessenv.ChatGPTApp})
	if worker["token"] == nil || worker["code"] != nil {
		t.Fatalf("setup: app-owned worker registration: %v", worker)
	}
	board, err := eng.Board(context.Background())
	if err != nil {
		t.Fatal("setup: recorded app identity:", err)
	}
	for _, row := range board["agents"].([]map[string]any) {
		if row["id"] == "queue-worker" {
			info, ok := row["agent"].(*core.AgentInfo)
			if !ok || info.Surface != harnessenv.ChatGPTApp {
				t.Fatalf("setup: worker has no bridge-derived app surface: %v", row)
			}
			return worker
		}
	}
	t.Fatal("setup: app-owned worker is absent from the board")
	return nil
}

func requireDormantSendFixture(t *testing.T, eng *engine.Engine, agent string) {
	t.Helper()
	if _, err := eng.Do(t.Context(), &core.Op{Kind: core.OpSweep, DeadAgents: []string{agent}}); err != nil {
		t.Fatal("setup: mark dormant:", err)
	}
	board, err := eng.Board(t.Context())
	if err != nil {
		t.Fatal("setup: read status:", err)
	}
	for _, row := range board["agents"].([]map[string]any) {
		if row["id"] == agent {
			if state := fmt.Sprint(row["status"]); state != string(core.StatusDormant) {
				t.Fatalf("setup: %s is %q, want dormant", agent, state)
			}
			return
		}
	}
	t.Fatalf("setup: %s is not dormant", agent)
}

func writeObservationQueue(t *testing.T, home, thread, text string) {
	t.Helper()
	rows := []map[string]any{{"id": "fixture-existing", "threadId": thread, "input": []map[string]string{{"type": "text", "text": text}}}}
	b, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(home, "pending.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func awaitObservationQueueCommand(t *testing.T, home string) {
	t.Helper()
	until := time.Now().Add(5 * time.Second)
	for {
		methods, err := os.ReadFile(filepath.Join(home, "methods"))
		_, queued := os.Stat(filepath.Join(home, "pending.json"))
		if err == nil && queued == nil && strings.Contains(string(methods), "thread/queue/list") {
			for _, method := range strings.Fields(string(methods)) {
				if method != "initialize" && method != "initialized" && method != "thread/read" && method != "thread/queue/list" {
					t.Fatalf("queue observer used a mutating method: %s", method)
				}
			}
			return
		}
		if time.Now().After(until) {
			t.Fatalf("setup: actual queue command/probe never ran: %v %v", err, queued)
		}
		<-time.After(10 * time.Millisecond)
	}
}

func assertQueueWakeView(t *testing.T, srv *httptest.Server, token any, admission, pending string) map[string]any {
	t.Helper()
	// The caller waits for the real app contact or the adapter observation.
	// One checkpoint is sufficient; a 10ms polling loop exhausted
	// the real read limit and hid the intended assertion behind E_RATE_LIMITED.
	out := toolCall(t, srv, "check_in", map[string]any{"token": token})
	board, ok := out["board"].(map[string]any)
	if !ok {
		t.Fatalf("setup: checkpoint has no board: %v", out)
	}
	var found map[string]any
	for _, a := range board["agents"].([]any) {
		row := a.(map[string]any)
		if row["id"] == "queue-worker" {
			found, _ = row["queue_wake"].(map[string]any)
		}
	}
	if found == nil || found["admission"] != admission || found["pending"] != pending {
		t.Fatalf("board lost actual queue observation %s/%s: %v", admission, pending, found)
	}
	return found
}

func awaitQueueWakeObservationAfter(t *testing.T, eng *engine.Engine, prior any) {
	t.Helper()
	stamp, ok := prior.(string)
	if !ok {
		t.Fatalf("setup: expected a serialized observation clock, got %v", prior)
	}
	previous, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		t.Fatal("setup: parse observation clock:", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		board, err := eng.Board(ctx)
		if err != nil {
			t.Fatal("read queue observation:", err)
		}
		for _, row := range board["agents"].([]map[string]any) {
			if row["id"] == "queue-worker" {
				wake, _ := row["queue_wake"].(core.Result)
				if at, ok := wake["observed_at"].(time.Time); ok && at.After(previous) {
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("setup: second queue observation did not arrive")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func awaitQueueAppContact(t *testing.T, contacts <-chan struct{}, dir, thread string) {
	t.Helper()
	select {
	case <-contacts:
	case <-time.After(5 * time.Second):
		t.Fatal("setup: real wake path never reached the stand-in app contact")
	}
	// Holds runs under the app-memo lock, before the loaded state is saved.
	// Receiving its callback is not completion: let the real lock release
	// establish that the writer is done before another send or TempDir cleanup.
	key := sha256.Sum256([]byte(thread))
	path := filepath.Join(dir, "app-opens", hex.EncodeToString(key[:])+".json.lock")
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal("setup: app contact did not hold its real memo lock:", err)
	}
	done := make(chan error, 1)
	go func() {
		err := paths.LockExclusive(f, true)
		if err == nil {
			paths.Unlock(f)
		}
		done <- errors.Join(err, f.Close())
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("app memo writer did not finish cleanly:", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("app contact returned but its memo writer never released the lock")
	}
}
