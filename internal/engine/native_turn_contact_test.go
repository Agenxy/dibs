// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/testcodexipc"
)

// Send through the real native IPC client and record the resulting fixture
// turn IDs in the same bounded local rollout surface the daemon will read.
func TestNativeTurnFailuresHoldFurtherDelivery(t *testing.T) {
	for _, tc := range []struct {
		name, code, errorText, want string
	}{
		{"context-full", "context_window_exceeded", "Error running remote compact task: fixture", "context is full"},
		{"compaction", "other", "Error running remote compact task: fixture", "reports a compaction failure"},
		{"context-full-18s", "context_window_exceeded", "Error running remote compact task: fixture", "context is full"},
		{"compaction-37s", "other", "Error running remote compact task: fixture", "reports a compaction failure"},
		{"context-code-only-18s", "context_window_exceeded", "context exhausted", "context is full"},
		{"tool-checkin-recovery", "context_window_exceeded", "Error running remote compact task: fixture", "context is full"},
		{"tool-called-37s", "context_window_exceeded", "Error running remote compact task: fixture", ""},
		{"different-id-37s", "context_window_exceeded", "Error running remote compact task: fixture", ""},
		{"other", "other", "private unknown failure details", "turns end immediately"},
		{"old-turn", "other", "private busy failure details", ""},
		{"different-id", "other", "private unrelated failure details", ""},
		{"auth-between-turns", "other", "private interrupted failure details", ""},
		{"wrong-thread", "other", "private unrelated thread details", ""},
		{"tool-called", "other", "private failure after a tool", ""},
		{"successful", "", "", ""},
		{"quiet-pair", "other", "private quiet failure", "turns end immediately"},
		{"recovery-probe", "other", "private recovery failure", "turns end immediately"},
		{"notify-recovery", "other", "private FYI recovery failure", "turns end immediately"},
		{"episode-binding", "other", "private binding failure", "turns end immediately"},
		{"episode-restart", "other", "private replay failure", "turns end immediately"},
		{"episode-sharing", "other", "private episode failure", "turns end immediately"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DIBS_DIR", t.TempDir())
			server := testcodexipc.Start(t, "sequence", nil)
			e, ctx, worker, sender := nativeAppEngine(t)
			e.SetHumanNotifier(noContactDesktop{})
			f := &acceptedContactFixture{e, ctx, server, worker, sender}
			path := filepath.Join(os.Getenv("CODEX_HOME"), "sessions",
				"rollout-fixture-"+testcodexipc.Thread+"_019ffe52-0eaf-7f60-81cc-6ab1298d76ec.jsonl")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = file.Close() })
			write := func(typ string, at time.Time, payload map[string]any) {
				t.Helper()
				if err := json.NewEncoder(file).Encode(map[string]any{
					"type": typ, "timestamp": at.UTC(), "payload": payload,
				}); err != nil {
					t.Fatal(err)
				}
			}
			header := testcodexipc.Thread
			if tc.name == "wrong-thread" {
				header = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
			}
			write("session_meta", time.Now(), map[string]any{"id": header})
			if tc.name == "compaction" {
				// A historical region larger than the read budget must not
				// prevent observing the current records in this continuation.
				if _, err := file.Seek(64<<20, 0); err != nil {
					t.Fatal(err)
				}
				if _, err := file.WriteString("\n"); err != nil {
					t.Fatal(err)
				}
			}
			kind := core.MsgRequest
			if tc.name == "notify-recovery" {
				kind = core.MsgNotify
			}
			var lastAccepted time.Time
			for n := 1; n <= 2; n++ {
				_, accepted := f.send(t, kind, n)
				lastAccepted = accepted
				id := fmt.Sprintf("turn-%d", n)
				start := accepted
				if tc.name == "old-turn" {
					start = start.Add(-time.Minute)
				}
				if strings.HasPrefix(tc.name, "different-id") {
					id = "unrelated-" + id
				}
				write("event_msg", start, map[string]any{"type": "task_started", "turn_id": id})
				if strings.HasPrefix(tc.name, "tool-called") {
					write("response_item", start, map[string]any{"type": "function_call", "name": "mcp__dibs__inbox"})
				}
				// Trace timestamps carry the measured slow-completion shape without
				// sleeping or making a real app turn.
				duration := time.Millisecond
				if strings.HasSuffix(tc.name, "18s") {
					duration = 18 * time.Second
				} else if strings.HasSuffix(tc.name, "37s") {
					duration = 37 * time.Second
				}
				complete := map[string]any{"type": "task_complete", "turn_id": id, "duration_ms": duration.Milliseconds()}
				if tc.errorText != "" {
					complete["error"] = map[string]any{"message": tc.errorText, "codex_error_info": tc.code}
				}
				write("event_msg", start.Add(duration), complete)
				if n == 1 && tc.name == "auth-between-turns" {
					if _, err := e.Inbox(ctx, worker); err != nil {
						t.Fatal(err)
					}
				}
			}
			if tc.name == "quiet-pair" {
				f.sweep(t, lastAccepted.Add(3*time.Second))
				deadline := time.Now().Add(time.Second)
				for {
					notices, err := e.PendingForHuman(ctx)
					if err != nil {
						t.Fatal(err)
					}
					if len(notices) == 1 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("two failed turns without a later send never raised contact")
					}
					<-time.After(5 * time.Millisecond)
				}
			}
			third := contactReplayDo(t, e, ctx, &core.Op{
				Kind: core.OpSendMessage, Token: sender, To: "worker", MsgType: kind, Body: "private third notice",
			})
			if third["msg_serial"] == nil {
				t.Fatalf("setup: third request not stored: %v", third)
			}
			waitWakeDone(t, e, "worker")
			if tc.want == "" {
				awaitNative(t, server, 3)
				return
			}
			wantInputs := 2
			if tc.name == "quiet-pair" {
				wantInputs = 3 // the sweep already set the hold before this new message
			}
			if got := len(server.Inputs()); got != wantInputs {
				t.Fatalf("native inputs after two failures: got %d, want %d", got, wantInputs)
			}
			if tc.name == "notify-recovery" {
				testNativeTurnRecovery(t, f, write, false)
				return
			}
			notices, err := e.PendingForHuman(ctx)
			if err != nil || len(notices) != 1 || notices[0].Contact == nil {
				t.Fatalf("two failed turns did not produce one contact alert: %v / %v", notices, err)
			}
			n := notices[0].Contact
			if !strings.Contains(n.OpenHint, tc.want) || n.OpenURL != "codex://threads/"+testcodexipc.Thread ||
				strings.Contains(n.OpenHint, tc.errorText) ||
				tc.want != "context is full" && strings.Contains(n.OpenHint, "context is full") {
				t.Fatalf("contact did not report only the proved reason and original thread: %+v", n)
			}
			if tc.name == "recovery-probe" {
				testNativeTurnRecovery(t, f, write, true)
				return
			}
			if tc.name == "tool-checkin-recovery" {
				testNativeToolCheckInRecovery(t, f, write)
				return
			}
			switch tc.name {
			case "episode-binding":
				testNativeEpisodeBinding(t, f)
				return
			case "episode-restart":
				testNativeEpisodeRestart(t, f)
				return
			case "episode-sharing":
				testNativeEpisodeSharing(t, f)
				return
			}
			led := e.led.(*memLedger)
			fenced, err := e.query(ctx, func() core.Result {
				for _, op := range led.ops {
					if op.Kind == "contact_diagnosed" && op.AgentID == "worker" {
						return core.Result{"fenced": true}
					}
				}
				return nil
			})
			if err != nil || fenced["fenced"] != true {
				t.Fatalf("real diagnosis append lacked its reader boundary: %v / %v", fenced, err)
			}
			replayed, replayCtx := replayContactEngine(t, e, ctx, led)
			persisted, err := replayed.PendingForHuman(replayCtx)
			if err != nil || len(persisted) != 1 || persisted[0].Contact == nil ||
				!strings.Contains(persisted[0].Contact.OpenHint, tc.want) {
				t.Fatalf("replay lost the contact diagnosis: %v / %v", persisted, err)
			}
			if _, err := e.Inbox(ctx, worker); err != nil {
				t.Fatal(err)
			}
			f.send(t, core.MsgRequest, wantInputs+1)
		})
	}
}

func testNativeToolCheckInRecovery(
	t *testing.T, f *acceptedContactFixture, write func(string, time.Time, map[string]any),
) {
	t.Helper()
	_, at := f.send(t, core.MsgRequest, 3)
	var probe harnessenv.CodexTurnReceipt
	if _, err := f.e.query(f.ctx, func() core.Result {
		w := f.e.nativeTurnWatches["worker"]
		if w.reason != core.ContactContextFull || w.probe.ID != "turn-3" {
			t.Errorf("setup: new mail did not probe an established hold: %+v", w)
		}
		probe = w.probe
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if t.Failed() {
		return
	}
	write("event_msg", at, map[string]any{"type": "task_started", "turn_id": probe.ID})
	write("response_item", at, map[string]any{
		"type": "function_call", "turn_id": probe.ID, "name": "mcp__dibs__check_in",
	})
	// Tool activity still proves progress without waiting for task_complete.
	if got := harnessenv.ObserveCodexTurns(testcodexipc.Thread, [2]harnessenv.CodexTurnReceipt{probe})[0]; got != (harnessenv.CodexTurnObservation{Normal: true}) {
		t.Fatalf("probe tool activity did not prove progress: %+v", got)
	}
	// Enter check_in through the actual engine operation, not the contact setter.
	contactReplayDo(t, f.e, f.ctx, &core.Op{Kind: core.OpAckBoard, Token: f.worker})
	r, err := f.e.query(f.ctx, func() core.Result {
		_, held := f.e.nativeTurnWatches["worker"]
		return core.Result{"held": held, "contact": f.e.recentAuthenticatedContact(f.e.state.Agents["worker"], time.Now())}
	})
	if err != nil || r["held"] != false || r["contact"] != true {
		t.Fatalf("authenticated probe check_in did not clear the hold: %v / %v", r, err)
	}
	f.send(t, core.MsgRequest, 4)
}

func testNativeTurnRecovery(
	t *testing.T, f *acceptedContactFixture, write func(string, time.Time, map[string]any), alreadyAlerted bool,
) {
	t.Helper()
	ledger := f.e.led.(*memLedger)
	diagnoses := func() int {
		t.Helper()
		r, err := f.e.query(f.ctx, func() core.Result {
			n := 0
			for _, op := range ledger.ops {
				if op.ContactReason != "" {
					n++
				}
			}
			return core.Result{"n": n}
		})
		if err != nil {
			t.Fatal(err)
		}
		return r["n"].(int)
	}
	retry := func() {
		t.Helper()
		if _, err := f.e.query(f.ctx, func() core.Result {
			f.e.retryWakeDecision("worker")
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		waitWakeDone(t, f.e, "worker")
	}
	record := func(id string, at time.Time, failed bool) {
		t.Helper()
		write("event_msg", at, map[string]any{"type": "task_started", "turn_id": id})
		done := map[string]any{"type": "task_complete", "turn_id": id, "duration_ms": 1}
		if failed {
			done["error"] = map[string]any{"message": "fixture failure", "codex_error_info": "other"}
		}
		write("event_msg", at.Add(time.Millisecond), done)
	}

	original := diagnoses()
	if (original > 0) != alreadyAlerted {
		t.Fatalf("setup: initial hold has %d diagnoses, want prior alert=%v", original, alreadyAlerted)
	}
	_, failed := f.send(t, core.MsgRequest, 3)
	record("turn-3", failed, true)
	retry()
	if n := len(f.ipc.Inputs()); n != 3 {
		t.Fatalf("old mail triggered another native input after the failed recovery probe: %d", n)
	}
	n := diagnoses()
	if alreadyAlerted && n != original || !alreadyAlerted && n != 1 {
		t.Fatalf("failed recovery probe should only raise a first diagnosis: before=%d after=%d", original, n)
	}
	original = n
	_, recovered := f.send(t, core.MsgRequest, 4)
	record("turn-4", recovered, false)
	retry()
	awaitNative(t, f.ipc, 5)
	if n := len(f.ipc.Inputs()); n != 5 {
		t.Fatalf("the normal probe did not restore ordinary delivery: %d", n)
	}
	if n := diagnoses(); n != original {
		t.Fatalf("recovery added a diagnosis: before=%d after=%d", original, n)
	}
}

func replayNativeEpisode(t *testing.T, f *acceptedContactFixture) *acceptedContactFixture {
	t.Helper()
	var ops []*core.Op
	if _, err := f.e.query(f.ctx, func() core.Result {
		ops = append(ops, f.e.led.(*memLedger).ops...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st := core.NewState("native-app", core.DefaultLimits())
	var history []core.Event
	for _, op := range ops {
		_, events, err := st.Apply(op, time.Now())
		if err != nil {
			t.Fatal("replay:", err)
		}
		history = append(history, events...)
	}
	e := New(st, &memLedger{ops: ops}, deadProber{}, history)
	e.SetHumanNotifier(noContactDesktop{})
	e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{
		filepath.Join(t.TempDir(), "codex"), "queue", "--thread", "{thread}", "--message", "{message}",
	}}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	stopWakeTimersOnCleanup(t, e)
	if _, err := e.Board(ctx); err != nil {
		t.Fatal(err)
	}
	waitWakeDone(t, e, "worker")
	return &acceptedContactFixture{e, ctx, f.ipc, f.worker, f.sender}
}

func testNativeEpisodeRestart(t *testing.T, f *acceptedContactFixture) {
	t.Helper()
	n := len(f.ipc.Inputs())
	replayed := replayNativeEpisode(t, f)
	if got := len(f.ipc.Inputs()); got != n {
		t.Fatalf("restart re-offered old mail despite an unresolved diagnosis: before=%d after=%d", n, got)
	}
	replayed.send(t, core.MsgRequest, n+1) // a real new message still probes the hold
}

func testNativeEpisodeBinding(t *testing.T, f *acceptedContactFixture) {
	t.Helper()
	contactReplayDo(t, f.e, f.ctx, &core.Op{
		Kind: core.OpBindSession, Token: f.worker, SessionID: "019ffe52-0eaf-7f60-81cc-6ab1298d76ec",
	})
	board, err := f.e.Board(f.ctx)
	if err != nil || board["contact_alerts"] != nil {
		t.Fatalf("binding change retained its diagnosed contact: %v / %v", board["contact_alerts"], err)
	}
}

func testNativeEpisodeSharing(t *testing.T, f *acceptedContactFixture) {
	t.Helper()
	snapshot := func() core.Result {
		t.Helper()
		r, err := f.e.query(f.ctx, func() core.Result {
			var c *core.ContactEscalation
			for _, contact := range f.e.state.Contacts {
				c = contact
			}
			return core.Result{
				"contacts": len(f.e.state.Contacts), "serial": c.Serial, "count": c.Count,
				"sender": fmt.Sprint(f.e.takeNotices("sender")),
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	before := snapshot()
	if !strings.Contains(before["sender"].(string), "turns end immediately") {
		t.Errorf("sender lacks the episode diagnosis: %v", before)
	}
	contactReplayDo(t, f.e, f.ctx, &core.Op{Kind: core.OpContactNotified, ContactSerial: before["serial"].(uint64)})
	if _, err := f.e.Inbox(f.ctx, f.sender); err != nil {
		t.Fatal(err)
	}
	f.send(t, core.MsgRequest, 3)
	after := snapshot()
	if after["contacts"] != 1 || after["count"] != before["count"].(int)+1 ||
		after["sender"] != "[]" {
		t.Errorf("new mail did not join the posted episode quietly: before=%v after=%v", before, after)
	}
	other := contactReplayDo(t, f.e, f.ctx, &core.Op{
		Kind: core.OpRegister, Name: "other", SessionID: "episode-other",
	})
	second := *f
	second.sender = other["token"].(string)
	msg, _ := second.send(t, core.MsgNotify, 4, "high")
	if r, err := f.e.query(f.ctx, func() core.Result {
		return core.Result{"notices": fmt.Sprint(f.e.takeNotices("other"))}
	}); err != nil || !strings.Contains(r["notices"].(string), "turns end immediately") {
		t.Fatalf("another sender did not receive its one diagnosis: %v / %v", r, err)
	}
	result := contactReplayDo(t, f.e, f.ctx, &core.Op{
		Kind: core.OpRespond, Token: second.sender, MsgSerial: msg, Disposition: "withdraw",
	})
	if result["state"] != "withdrawn" {
		t.Fatalf("notify withdrawal failed: %v", result)
	}
	if got := snapshot(); got["count"] != before["count"].(int)+1 {
		t.Fatalf("withdrawal did not decrement shared contact count: %v", got)
	}
	for _, current := range []*acceptedContactFixture{f, replayNativeEpisode(t, f)} {
		hook, err := current.e.HookPoll(current.ctx, "episode-other", "Stop", "", false, false)
		if err != nil || hook["decision"] == "block" || strings.Contains(fmt.Sprint(hook), "outstanding update units") {
			t.Fatalf("withdrawn sender warning returned in Stop: %v / %v", hook, err)
		}
	}
	second.send(t, core.MsgNotify, 5, "high")
	if r, err := f.e.query(f.ctx, func() core.Result {
		return core.Result{"notices": fmt.Sprint(f.e.takeNotices("other"))}
	}); err != nil || r["notices"] != "[]" {
		t.Fatalf("same-episode mail resurrected the retained withdrawn-source warning: %v / %v", r, err)
	}
}
