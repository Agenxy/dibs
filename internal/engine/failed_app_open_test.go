// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/testcodexipc"
	"github.com/agenxy/dibs/internal/testexec"
)

// Enter through send, the actual no-owner IPC response, queue admission and
// the OS-open callback. A configured queue is not a usable route after that
// callback fails. No test can open the operator's real app.
func TestFailedColdAppOpenAlertsSenderAndHuman(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "codex")
	if b, err := exec.Command("go", "build", "-o", binary, "../wakeexec/testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v: %s", err, b)
	}
	testexec.PrepareExecutable(t, exec.Command(binary, "--version"))
	for _, mode := range []string{"failed", "opened", "read-before-failure", "rebound-before-failure", "reconfigured-before-failure"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("DIBS_DIR", t.TempDir())
			const senderThread = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
			server := testcodexipc.Start(t, "active", nil)
			server.Owns = func(thread string) bool { return thread == senderThread }
			e, ctx, worker, sender := nativeAppEngine(t)
			senderID := "sender"
			if mode == "failed" {
				senderID = "app-sender"
				result, err := e.Do(ctx, &core.Op{
					Kind: core.OpRegister, Name: senderID, SessionID: senderThread,
					Agent: &core.AgentInfo{Harness: "Codex", Surface: harnessenv.ChatGPTApp},
				})
				if err != nil || result["error"] != nil {
					t.Fatalf("setup sender route: %v / %v", result, err)
				}
				sender = result["token"].(string)
				route, err := e.query(ctx, func() core.Result {
					l := e.state.Agents[senderID]
					return core.Result{"native": e.nativeAppRoute(l), "thread": threadIDOf(l)}
				})
				if err != nil || route["native"] != true || route["thread"] != senderThread {
					t.Fatalf("setup: original sender has no native route: %v / %v", route, err)
				}
				t.Cleanup(func() { waitWakeDone(t, e, senderID) })
			}
			e.SetHumanNotifier(noContactDesktop{})
			e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{
				binary, "queue", "--thread", "{thread}", "--message", "{message}",
			}}})
			previous := shower
			entered, release := make(chan struct{}, 1), make(chan struct{})
			shower = &harnessenv.Shower{
				Holds: func(string) bool { return false },
				Open: func(argv []string) error {
					if len(argv) != 3 || argv[2] != "codex://threads/wake/"+testcodexipc.Thread {
						t.Errorf("wrong original app/thread: %v", argv)
					}
					entered <- struct{}{}
					<-release
					if mode == "opened" {
						return nil
					}
					return errors.New("PRIVATE-OS-FAILURE")
				},
			}
			t.Cleanup(func() { shower = previous })
			closed := false
			t.Cleanup(func() {
				if !closed {
					close(release)
				}
			})
			sent, err := e.Do(ctx, &core.Op{
				Kind: core.OpSendMessage, Token: sender,
				To: "worker", MsgType: core.MsgRequest, Body: "PRIVATE-REQUEST",
			})
			if err != nil || sent["error"] != nil || sent["msg_serial"] == nil {
				t.Fatalf("setup send: %v / %v", sent, err)
			}
			serial := sent["msg_serial"].(uint64)
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("setup: app opener not reached")
			}
			if len(server.Inputs()) != 0 {
				t.Fatal("setup: no-owner path submitted native input")
			}
			switch mode {
			case "read-before-failure":
				if _, err := e.Inbox(ctx, worker); err != nil {
					t.Fatal(err)
				}
			case "rebound-before-failure":
				if _, err := e.Do(ctx, &core.Op{
					Kind: core.OpBindSession, Token: worker,
					SessionID: "019ffe52-0eaf-7f60-81cc-6ab1298d76ec",
				}); err != nil {
					t.Fatal(err)
				}
			case "reconfigured-before-failure":
				e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{
					filepath.Join(t.TempDir(), "codex"), "queue", "--thread", "{thread}", "--message", "{message}",
				}}})
			}
			close(release)
			closed = true
			waitWakeDone(t, e, "worker")
			awaitQueuedContactProbe(t, e)
			got, err := e.query(ctx, func() core.Result {
				m := e.state.Messages[serial]
				return core.Result{
					"contact": m.ContactEscalatedAt,
					"state":   m.State, "contacts": len(e.state.Contacts),
					"sender_notice": fmt.Sprint(e.takeNotices(senderID)),
					"board":         fmt.Sprint(e.state.Board()["contact_alerts"]),
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			if mode != "failed" {
				if got["contact"] != uint64(0) || got["contacts"] != 0 || got["sender_notice"] != "[]" {
					t.Fatalf("stale or successful open raised contact: %v", got)
				}
				return
			}
			awaitNative(t, server, 1)
			waitWakeDone(t, e, senderID)
			if calls := server.Inputs(); len(calls) != 1 || calls[0]["params"].(map[string]any)["conversationId"] != senderThread {
				t.Fatalf("failure notice did not wake the original sender through its actual native route: %v", calls)
			}
			if got["contact"] == uint64(0) || got["contacts"] != 1 || got["state"] != core.MsgStatePending {
				t.Fatalf("failed open left unread request silently queued: %v", got)
			}
			notice := got["sender_notice"].(string)
			for _, want := range []string{"queued", "codex://threads/" + testcodexipc.Thread, "contact"} {
				if !strings.Contains(notice, want) {
					t.Fatalf("sender lacks %q: %s", want, notice)
				}
			}
			for _, private := range []string{"PRIVATE-REQUEST", "PRIVATE-OS-FAILURE"} {
				if strings.Contains(notice, private) {
					t.Fatalf("sender notice copied private content: %s", notice)
				}
			}
			// The warning is contact news, independent of the pending envelope.
			// Reconstruct it from ledgered state, then prove the authenticated
			// contact-read prefix survives another replay without repeating it.
			led, ok := e.led.(*memLedger)
			if !ok {
				t.Fatal("setup: expected retained fixture ledger")
			}
			restarted, restartedCtx := replayContactEngine(t, e, ctx, led)
			updates := readContactUpdates(t, restarted, restartedCtx, sender, "inbox")
			if !strings.Contains(updates, "codex://threads/"+testcodexipc.Thread) {
				t.Fatalf("restart lost sender warning: %s", updates)
			}
			readContactUpdates(t, e, ctx, sender, "inbox")
			readAgain, readAgainCtx := replayContactEngine(t, e, ctx, led)
			if updates := readContactUpdates(t, readAgain, readAgainCtx, sender, "inbox"); strings.Contains(updates, "codex://threads/"+testcodexipc.Thread) {
				t.Fatalf("restart repeated read contact warning: %s", updates)
			}
		})
	}
}

func TestStrandedQueueOpenHasRecoveryWindow(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "codex")
	if b, err := exec.Command("go", "build", "-o", binary, "../wakeexec/testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v: %s", err, b)
	}
	testexec.PrepareExecutable(t, exec.Command(binary, "--version"))
	for _, mode := range []string{
		"fresh", "recent-open-then-read", "recent-open-unread", "retry-unread",
		"repeat-while-waiting", "active-then-idle", "queue-consumed", "inconclusive",
	} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("DIBS_DIR", dir)
			testcodexipc.Start(t, "no-owner", nil)
			e, ctx, worker, sender := nativeAppEngine(t)
			e.SetHumanNotifier(noContactDesktop{})
			e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{
				binary, "queue", "--thread", "{thread}", "--message", "{message}",
			}}})
			previous := shower
			opens := 0
			shower = &harnessenv.Shower{
				Holds: func(string) bool { return false },
				Open:  func([]string) error { opens++; return nil },
			}
			t.Cleanup(func() { shower = previous })
			send := func() uint64 {
				t.Helper()
				r, err := e.Do(ctx, &core.Op{
					Kind: core.OpSendMessage, Token: sender,
					To: "worker", MsgType: core.MsgRequest, Body: "PRIVATE-STRANDED-ASK",
				})
				if err != nil || r["error"] != nil || r["msg_serial"] == nil {
					t.Fatalf("setup send: %v / %v", r, err)
				}
				waitWakeDone(t, e, "worker")
				return r["msg_serial"].(uint64)
			}
			send()
			if opens != 1 {
				t.Fatalf("setup: first send did not open app: %d", opens)
			}
			queue := filepath.Join(os.Getenv("CODEX_HOME"), "pending.json")
			raw, err := os.ReadFile(queue)
			if err != nil {
				t.Fatal(err)
			}
			var rows []map[string]any
			if json.Unmarshal(raw, &rows) != nil || len(rows) != 1 {
				t.Fatalf("setup queue: %s", raw)
			}
			var issued time.Time
			if mode != "fresh" {
				// Only the external queue and open-receipt fixtures are aged.
				// The second send still enters through the real admission path.
				issued = time.Now().Add(-20 * time.Minute).UTC().Truncate(time.Second)
				if mode == "inconclusive" {
					issued = time.Now().Add(-12 * time.Minute).UTC().Truncate(time.Second)
				}
				rows[0]["input"] = []map[string]string{{"type": "text", "text": "Dibs: request notice issued at " + issued.Format(time.RFC3339) + ". It may already be handled."}}
				raw, err = json.Marshal(rows)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(queue, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ageOpenMemo := func() {
				t.Helper()
				at := time.Now().Add(-20 * time.Minute).UTC()
				entries, err := os.ReadDir(filepath.Join(dir, "app-opens"))
				if err != nil {
					t.Fatal(err)
				}
				aged := 0
				for _, entry := range entries {
					if filepath.Ext(entry.Name()) != ".json" {
						continue
					}
					path := filepath.Join(dir, "app-opens", entry.Name())
					raw, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					var memo map[string]any
					if json.Unmarshal(raw, &memo) != nil {
						t.Fatal("setup: bad open memo")
					}
					memo["opened_at"] = at
					raw, err = json.Marshal(memo)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, raw, 0o600); err != nil {
						t.Fatal(err)
					}
					aged++
				}
				if aged != 1 {
					t.Fatalf("setup aged %d open receipts", aged)
				}
			}
			if mode == "retry-unread" {
				ageOpenMemo()
			}
			var log economyLog
			previousLog := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelInfo})))
			t.Cleanup(func() { slog.SetDefault(previousLog) })
			before := time.Now()
			serial := send()
			after := time.Now()
			snapshot := func() core.Result {
				t.Helper()
				got, err := e.query(ctx, func() core.Result {
					m := e.state.Messages[serial]
					c := e.state.Contacts[m.ContactEscalatedAt]
					hint := ""
					if c != nil {
						n, _ := e.contactNotice(c)
						hint = n.Contact.OpenHint
					}
					return core.Result{
						"contact": m.ContactEscalatedAt, "resolved": c != nil && !c.ResolvedAt.IsZero(),
						"sender_notice": fmt.Sprint(e.takeNotices("sender")),
						"contacts":      len(e.state.Contacts), "hint": hint,
					}
				})
				if err != nil {
					t.Fatal(err)
				}
				return got
			}
			sweep := func(now time.Time) {
				t.Helper()
				if _, err := e.query(ctx, func() core.Result { e.sweep(now); return nil }); err != nil {
					t.Fatal(err)
				}
				awaitQueuedContactProbe(t, e)
			}
			raw, err = os.ReadFile(queue)
			if err != nil {
				t.Fatal(err)
			}
			if json.Unmarshal(raw, &rows) != nil || len(rows) != 1 {
				t.Fatalf("retained wake was duplicated: %s", raw)
			}
			wantOpens := 1
			if mode == "retry-unread" {
				wantOpens = 2
			}
			if opens != wantOpens {
				t.Fatalf("setup: opens=%d want %d", opens, wantOpens)
			}
			if got := snapshot(); got["contact"] != uint64(0) {
				t.Fatalf("paged immediately, before the app-open recovery window: %v", got)
			}
			if mode == "repeat-while-waiting" {
				ageOpenMemo() // the next wake could open again if it ignored the pending window
				serial = send()
				if opens != wantOpens {
					t.Fatalf("same retained item opened again during its recovery window: %d opens", opens)
				}
			}
			sweep(before.Add(harnessenv.AppOpenRetryAfter - 10*time.Second))
			if got := snapshot(); got["contact"] != uint64(0) {
				t.Fatalf("paged before the app-open recovery window elapsed: %v", got)
			}
			if mode == "recent-open-then-read" {
				if _, err := e.Inbox(ctx, worker); err != nil {
					t.Fatal(err)
				}
			}
			due := after.Add(harnessenv.AppOpenRetryAfter + time.Second)
			if mode == "inconclusive" {
				path := filepath.Join(os.Getenv("CODEX_HOME"), "thread-observation.json")
				if err := os.WriteFile(path, []byte(`{"updatedAt":0}`), 0o600); err != nil {
					t.Fatal("setup: set inconclusive app evidence:", err)
				}
				for _, beforeCap := range []time.Time{due, issued.Add(29 * time.Minute)} {
					sweep(beforeCap)
					if got := snapshot(); got["contact"] != uint64(0) {
						t.Fatalf("paged before the inconclusive-evidence cap: %v", got)
					}
				}
				sweep(issued.Add(31 * time.Minute))
				got := snapshot()
				if got["contact"] == uint64(0) || got["contacts"] != 1 ||
					!strings.Contains(got["hint"].(string), "could not confirm the thread's state") {
					t.Fatalf("cap should raise one honest contact alert: %v", got)
				}
				sweep(issued.Add(34 * time.Minute))
				if got := snapshot(); got["contacts"] != 1 {
					t.Fatalf("persistent uncertainty raised another contact alert: %v", got)
				}
				return
			}
			if mode == "queue-consumed" {
				if err := os.WriteFile(queue, []byte("[]"), 0o600); err != nil {
					t.Fatal("setup: consume the queued item:", err)
				}
			}
			if mode == "active-then-idle" {
				path := filepath.Join(os.Getenv("CODEX_HOME"), "thread-observation.json")
				observation := fmt.Appendf(nil, "{\"updatedAt\":%d}", due.Add(-time.Second).Unix())
				if err := os.WriteFile(path, observation, 0o600); err != nil {
					t.Fatal("setup: record active thread:", err)
				}
				sweep(due)
				if got := snapshot(); got["contact"] != uint64(0) {
					t.Fatalf("paged the human while the thread was active: %v", got)
				}
				due = due.Add(3 * time.Minute)
				active := fmt.Appendf(nil, "{\"updatedAt\":%d,\"status\":{\"type\":\"active\"}}", due.Add(-time.Hour).Unix())
				if err := os.WriteFile(path, active, 0o600); err != nil {
					t.Fatal("setup: record the explicit active status:", err)
				}
				sweep(due)
				if got := snapshot(); got["contact"] != uint64(0) {
					t.Fatalf("paged the human despite explicit active status: %v", got)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal("setup: return the app to idle:", err)
				}
				due = due.Add(3 * time.Minute)
			}
			sweep(due)
			got := snapshot()
			if mode == "fresh" || mode == "recent-open-then-read" || mode == "queue-consumed" {
				if got["contact"] != uint64(0) {
					t.Fatalf("fresh queue or read mail raised contact: %v", got)
				}
				return
			}
			if got["contact"] == uint64(0) {
				t.Fatalf("unread retained queue lost its contact after recovery: %v", got)
			}
			if !strings.Contains(log.String(), "enqueue suppressed, delivery unconfirmed") {
				t.Fatalf("retention invisible at INFO: %s", log.String())
			}
			if !strings.Contains(got["sender_notice"].(string), "codex://threads/"+testcodexipc.Thread) {
				t.Fatalf("sender lacks exact thread: %v", got)
			}
			if _, err := e.Inbox(ctx, worker); err != nil {
				t.Fatal(err)
			}
			if got := snapshot(); got["resolved"] != true {
				t.Fatalf("late read did not resolve contact: %v", got)
			}
		})
	}
}

func awaitQueuedContactProbe(t *testing.T, e *Engine) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		e.wakeWorkers.mu.Lock()
		running := false
		for _, w := range e.wakeWorkers.active {
			running = running || w.Attempt == "queued-contact"
		}
		e.wakeWorkers.mu.Unlock()
		if !running {
			return
		}
		select {
		case <-tick.C:
		case <-deadline:
			t.Fatal("the queued-contact probe did not finish")
		}
	}
}

// No opener is attempted for an ended Claude session, so no failed-open
// contact or sender notice may be manufactured. The pending request stays stored.
func TestEndedClaudeWaitDoesNotInventAnOpenFailureForSender(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newReopenContactFixture(t)
		sent := f.do(t, &core.Op{Kind: core.OpSendMessage, Token: f.sender, To: "lead", MsgType: core.MsgRequest, Body: "PRIVATE-CLAUDE-ASK"})
		serial := sent["msg_serial"].(uint64)
		synctest.Sleep(time.Second)
		synctest.Wait()
		got, err := f.e.query(f.ctx, func() core.Result {
			m := f.e.state.Messages[serial]
			return core.Result{
				"contact": m.ContactEscalatedAt, "news": fmt.Sprint(f.e.takeNotices("asker")),
				"retained": m.Body == "PRIVATE-CLAUDE-ASK", "reason": f.e.appBindings["lead"].reason,
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		if got["contact"] != uint64(0) || got["retained"] != true || got["reason"] != "waiting-thread-open" {
			t.Fatalf("ended-session wait dropped mail or invented an open failure: %v", got)
		}
		if strings.Contains(got["news"].(string), "PRIVATE-CLAUDE-ASK") {
			t.Fatal("sender notice copied private mail")
		}
		select {
		case argv := <-f.opened:
			t.Fatalf("ended session received app contact: %q", argv)
		default:
		}
	})
}

func TestClosedHarnessSendDoesNotClaimDelivery(t *testing.T) {
	e, ctx := newEconomyNoteEngine(t, economyDeadProcess{})
	const thread = "6336088d-1111-4222-8333-444455556666"
	worker, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "closed", Nonce: "closed-provenance", PID: 4242,
		SessionID: thread, AgentKind: core.KindPersistent,
	})
	if err != nil || worker["agent_id"] != "closed" {
		t.Fatalf("setup worker: %v %v", worker, err)
	}
	sender, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "sender", Nonce: "closed-asker"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: sender["token"].(string), To: "closed", MsgType: core.MsgRequest, Body: "private", DeadlineSec: 600})
	if err != nil {
		t.Fatal(err)
	}
	if r["ok"] != true || r["msg_serial"] == nil || r["deadline_pending"] != true || r["response_window_s"] != 600 {
		t.Fatalf("setup: send did not preserve the request and its response window: %v", r)
	}
	text := fmt.Sprint(r["note"])
	if !strings.Contains(text, "No started turn or read mail is confirmed") || !strings.Contains(text, thread) || strings.Contains(text, "delivered on its next start") {
		t.Fatalf("send invented delivery or omitted exact session: %s", text)
	}
	if !strings.Contains(text, "may raise a contact alert") || !strings.Contains(text, "choose another agent") {
		t.Fatalf("send did not explain the contact alert or offer a reachable recipient: %s", text)
	}
}
