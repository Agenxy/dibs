package mcp

import (
	"context"
	"encoding/json"
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
)

// Real MCP send -> writer planner -> native command/probe -> board and send
// receipts. Only the app contact and subprocess harness are fixtures. The same
// test compiles on the parent commit: it cannot seed the new observation cache.
func TestQueueWakeObservationsThroughMCPAndActualCommand(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "codex")
	if b, err := exec.Command("go", "build", "-o", binary, "../wakeexec/testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("setup: fixture build: %v: %s", err, b)
	}
	for _, mode := range []string{"admitted", "pending-aged", "probe-unknown", "legacy-age", "fallback", "rebound"} {
		t.Run(mode, func(t *testing.T) { queueWakeObservationCase(t, binary, mode) })
	}
}

func queueWakeObservationCase(t *testing.T, binary, mode string) {
	t.Helper()
	const thread = "0199a0b1-c2d3-4e5f-8a9b-0c1d2e3f4a5b"
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", home)
	t.Setenv("DIBS_DIR", t.TempDir())
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
	worker := call("register", map[string]any{
		"name": "queue-worker", "nonce": "queue-observation-worker-nonce",
		"session_id": thread, "harness": "Codex", "surface": harnessenv.ChatGPTApp, "cwd": home,
	})
	sender := call("register", map[string]any{"name": "queue-sender", "nonce": "queue-observation-sender-nonce"})
	token := sender["token"]
	call("check_in", map[string]any{"token": token})
	send := func() map[string]any {
		t.Helper()
		if _, err := eng.Do(context.Background(), &core.Op{Kind: core.OpSweep, DeadAgents: []string{"queue-worker"}}); err != nil {
			t.Fatal("setup: dormant worker:", err)
		}
		return call("send", map[string]any{"token": token, "to": "queue-worker", "type": "question", "body": "private observation fixture"})
	}
	if mode == "pending-aged" || mode == "legacy-age" {
		text := "Dibs: a new question is waiting."
		if mode == "pending-aged" {
			text = "Dibs: question notice issued at " + time.Now().Add(-13*time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339) + ". It may already be handled."
		}
		writeObservationQueue(t, home, thread, text)
	}
	send()
	// Confirm the real subprocess door ran before checking a new field. On old
	// code the setup remains green, and only the missing visibility is RED.
	awaitObservationQueueCommand(t, home)
	wantAdmission, wantPending := "accepted", "unknown"
	if mode == "pending-aged" || mode == "legacy-age" {
		wantAdmission, wantPending = "retained", "pending"
	}
	view := awaitQueueWakeView(t, srv, token, wantAdmission, wantPending)
	awaitQueueAppContact(t, contacts)
	if mode == "probe-unknown" {
		t.Setenv("DIBS_QUEUE_PROBE_FAIL", "1")
		send()
		view = awaitQueueWakeView(t, srv, token, "retained", "unknown")
		awaitQueueAppContact(t, contacts)
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
	awaitQueueAppContact(t, contacts)
	received, ok := receipt["queue_wake"].(map[string]any)
	if !ok || received["thread_state"] != "unknown" || received["observed_at"] == nil {
		t.Fatalf("send receipt lost the adapter observation: %v", receipt)
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
		view = awaitQueueWakeView(t, srv, token, "unconfirmed", "unknown")
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
				if method != "initialize" && method != "initialized" && method != "thread/queue/list" {
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

func awaitQueueWakeView(t *testing.T, srv *httptest.Server, token any, admission, pending string) map[string]any {
	t.Helper()
	until := time.Now().Add(5 * time.Second)
	var found map[string]any
	for {
		out := toolCall(t, srv, "check_in", map[string]any{"token": token})
		board, ok := out["board"].(map[string]any)
		if !ok {
			t.Fatalf("setup: checkpoint has no board: %v", out)
		}
		for _, a := range board["agents"].([]any) {
			row := a.(map[string]any)
			if row["id"] == "queue-worker" {
				found, _ = row["queue_wake"].(map[string]any)
			}
		}
		if found != nil && found["admission"] == admission && found["pending"] == pending {
			return found
		}
		if time.Now().After(until) {
			t.Fatalf("board lost actual queue observation %s/%s: %v", admission, pending, found)
		}
		<-time.After(10 * time.Millisecond)
	}
}

func awaitQueueAppContact(t *testing.T, contacts <-chan struct{}) {
	t.Helper()
	select {
	case <-contacts:
	case <-time.After(5 * time.Second):
		t.Fatal("setup: real wake path never reached the stand-in app contact")
	}
}
