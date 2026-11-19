// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/codexipc"
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/testcodexipc"
	"github.com/agenxy/dibs/internal/testexec"
)

// The restart path must not visit quiet chats just to preload them. Drive
// afterAppReturn with registered agents and actual owed mail; only the app's
// native socket, process observation and navigation are fixtures.
func TestRestartReturnOpensOnlyNonArchivedAgentsWithMailToDeliver(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "codex")
	if raw, err := exec.Command("go", "build", "-o", binary, "../wakeexec/testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("queue fixture: %v %s", err, raw)
	}
	testexec.PrepareExecutable(t, exec.Command(binary, "--version"))

	for _, scenario := range []string{"quiet", "held", "archived", "consumed"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("DIBS_DIR", t.TempDir())
			app := testcodexipc.Start(t, "idle", nil)
			var mu sync.Mutex
			loaded := map[string]bool{}
			var opens []string
			var turns int
			app.Owns = func(thread string) bool { mu.Lock(); defer mu.Unlock(); return loaded[thread] }
			priorEpoch, priorOwned, priorShower := appRestartEpoch.Load(), appThreadOwned, shower
			appRestartEpoch.Store(func() (string, bool) { return "app:B", true })
			appThreadOwned = codexipc.Owned
			shower = &harnessenv.Shower{
				Ownership: func(string) harnessenv.ThreadOwnership {
					return harnessenv.ThreadOwnership{Running: true, Known: true, Epoch: "app:B"}
				},
				Open: func(argv []string) error {
					thread := strings.TrimPrefix(argv[len(argv)-1], "codex://threads/wake/")
					admitted, err := consumeReturnQueue(thread)
					mu.Lock()
					defer mu.Unlock()
					opens = append(opens, argv[len(argv)-1])
					turns += len(admitted)
					if len(admitted) > 0 {
						loaded[thread] = true
					}
					return err
				},
			}
			t.Cleanup(func() { appRestartEpoch.Store(priorEpoch); appThreadOwned, shower = priorOwned, priorShower })
			e, ctx, worker, sender := nativeAppEngine(t)
			e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{binary, "queue", "--thread", "{thread}", "--message", "{message}"}}})
			const quiet = "0199a0b1-c2d3-4e5f-8a9b-0c1d2e3f4a5b"
			result, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "quiet", SessionID: quiet, AgentKind: core.KindPersistent, Agent: &core.AgentInfo{Harness: "Codex", Surface: harnessenv.ChatGPTApp}})
			if err != nil || result["agent_id"] != "quiet" {
				t.Fatalf("setup quiet agent: %v %v", result, err)
			}
			if _, err = e.Do(ctx, &core.Op{Kind: core.OpSweep, DeadAgents: []string{"quiet"}}); err != nil {
				t.Fatal(err)
			}
			e.appSettled.Store("app:A")
			if scenario == "archived" {
				_, err = e.query(ctx, func() core.Result {
					a := e.state.Agents["worker"]
					a.Status = core.StatusArchived
					a.ArchivedAt = time.Now()
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			var serial uint64
			if scenario != "quiet" {
				serial = nativeAppSend(t, e, ctx, sender)
				waitWakeDone(t, e, "worker")
			}
			if scenario == "consumed" {
				var once sync.Once
				appThreadOwned = func(ctx context.Context, thread string) (bool, error) {
					once.Do(func() {
						if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckMessage, Token: worker, MsgSerial: serial}); err != nil {
							t.Fatalf("setup consume mail: %v", err)
						}
					})
					return codexipc.Owned(ctx, thread)
				}
			}
			e.afterAppReturn(ctx, "app:B", nil, 0)
			waitWakeDone(t, e, "worker")
			mu.Lock()
			got := append([]string(nil), opens...)
			mu.Unlock()
			want := ""
			if scenario == "held" {
				want = "codex://threads/wake/" + testcodexipc.Thread
			}
			if strings.Join(got, " ") != want {
				t.Fatalf("restart navigated %q; only current non-archived delivery may navigate, want %q", got, want)
			}
			if scenario == "held" && (turns != 1 || len(app.Inputs()) != 0) {
				t.Fatalf("held mail delivered queued=%d native=%d, want exactly one queued turn", turns, len(app.Inputs()))
			}
			if scenario == "archived" {
				result, err = e.query(ctx, func() core.Result { return core.Result{"mail": len(e.state.Messages)} })
				if err != nil || result["mail"].(int) == 0 {
					t.Fatalf("archived mail was lost: %v %v", result, err)
				}
			}
			if scenario == "consumed" {
				board, err := e.Board(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if board["app_binding_alerts"] != nil {
					t.Fatal("handled mail was misdiagnosed as a stale binding")
				}
			}
		})
	}
}
