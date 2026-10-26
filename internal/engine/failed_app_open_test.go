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
					if len(argv) != 3 || argv[2] != "codex://threads/"+testcodexipc.Thread {
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

func TestStrandedQueueSecondWakeReopensAndEscalates(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "codex")
	if b, err := exec.Command("go", "build", "-o", binary, "../wakeexec/testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v: %s", err, b)
	}
	testexec.PrepareExecutable(t, exec.Command(binary, "--version"))
	for _, old := range []bool{false, true} {
		t.Run(fmt.Sprintf("old=%v", old), func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("DIBS_DIR", dir)
			testcodexipc.Start(t, "no-owner", nil)
			e, ctx, _, sender := nativeAppEngine(t)
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
			if old {
				// Only the external queue and open-receipt fixtures are aged.
				// The second send still enters through the real admission path.
				at := time.Now().Add(-20 * time.Minute).UTC()
				rows[0]["input"] = []map[string]string{{"type": "text", "text": "Dibs: request notice issued at " + at.Format(time.RFC3339) + ". It may already be handled."}}
				raw, err = json.Marshal(rows)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(queue, raw, 0o600); err != nil {
					t.Fatal(err)
				}
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
			var log economyLog
			previousLog := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelInfo})))
			t.Cleanup(func() { slog.SetDefault(previousLog) })
			serial := send()
			got, err := e.query(ctx, func() core.Result {
				return core.Result{
					"contact":       e.state.Messages[serial].ContactEscalatedAt,
					"sender_notice": fmt.Sprint(e.takeNotices("sender")),
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			raw, err = os.ReadFile(queue)
			if err != nil {
				t.Fatal(err)
			}
			if json.Unmarshal(raw, &rows) != nil || len(rows) != 1 {
				t.Fatalf("retained wake was duplicated: %s", raw)
			}
			if !old {
				if opens != 1 || got["contact"] != uint64(0) {
					t.Fatalf("fresh queue re-opened/escalated: opens=%d %v", opens, got)
				}
				return
			}
			if opens != 2 || got["contact"] == uint64(0) {
				t.Fatalf("stranded wake suppressed recovery/contact: opens=%d %v", opens, got)
			}
			if !strings.Contains(log.String(), "enqueue suppressed, delivery unconfirmed") {
				t.Fatalf("retention invisible at INFO: %s", log.String())
			}
			if !strings.Contains(got["sender_notice"].(string), "codex://threads/"+testcodexipc.Thread) {
				t.Fatalf("sender lacks exact thread: %v", got)
			}
		})
	}
}

func TestFailedClaudeReopenNotifiesOriginalSender(t *testing.T) {
	f := newReopenContactFixture(t)
	sent := f.do(t, &core.Op{Kind: core.OpSendMessage, Token: f.sender, To: "lead", MsgType: core.MsgRequest, Body: "PRIVATE-CLAUDE-ASK"})
	serial := sent["msg_serial"].(uint64)
	select {
	case <-f.opened:
	case <-time.After(2 * time.Second):
		t.Fatal("setup: no real reopen callback")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		got, err := f.e.query(f.ctx, func() core.Result {
			return core.Result{"contact": f.e.state.Messages[serial].ContactEscalatedAt, "news": fmt.Sprint(f.e.takeNotices("asker"))}
		})
		if err != nil {
			t.Fatal(err)
		}
		news := got["news"].(string)
		if got["contact"] != uint64(0) && strings.Contains(news, "6336088d-1111-4222-8333-444455556666") {
			if strings.Contains(news, "PRIVATE-CLAUDE-ASK") {
				t.Fatal("notice copied private body")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("failed reopen never told original sender which session to open: %v", got)
		}
		<-time.After(5 * time.Millisecond)
	}
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
	r, err := e.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: sender["token"].(string), To: "closed", MsgType: core.MsgRequest, Body: "private"})
	if err != nil {
		t.Fatal(err)
	}
	text := fmt.Sprint(r["note"])
	if !strings.Contains(text, "No started turn or read mail is confirmed") || !strings.Contains(text, thread) || strings.Contains(text, "delivered on its next start") {
		t.Fatalf("send invented delivery or omitted exact session: %s", text)
	}
}
