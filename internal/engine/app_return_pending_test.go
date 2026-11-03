// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/codexipc"
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/testcodexipc"
)

// The restart path must not visit quiet chats just to preload them. Drive
// afterAppReturn with registered agents and actual owed mail; only the app's
// native socket, process observation and navigation are fixtures.
func TestRestartReturnOpensOnlyNonArchivedAgentsWithMailToDeliver(t *testing.T) {
	for _, scenario := range []string{"quiet", "held", "archived", "consumed"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("DIBS_DIR", t.TempDir())
			app := testcodexipc.Start(t, "idle", nil)
			var mu sync.Mutex
			loaded := map[string]bool{}
			var opens []string
			app.Owns = func(thread string) bool { mu.Lock(); defer mu.Unlock(); return loaded[thread] }
			priorEpoch, priorOwned, priorShower := appRestartEpoch.Load(), appThreadOwned, shower
			appRestartEpoch.Store(func() (string, bool) { return "app:B", true })
			appThreadOwned = codexipc.Owned
			shower = &harnessenv.Shower{
				Ownership: func(string) harnessenv.ThreadOwnership {
					return harnessenv.ThreadOwnership{Running: true, Known: true, Epoch: "app:B"}
				},
				Open: func(argv []string) error {
					mu.Lock()
					defer mu.Unlock()
					url := argv[len(argv)-1]
					opens = append(opens, url)
					loaded[strings.TrimPrefix(url, "codex://threads/")] = true
					return nil
				},
			}
			t.Cleanup(func() { appRestartEpoch.Store(priorEpoch); appThreadOwned, shower = priorOwned, priorShower })
			e, ctx, worker, sender := nativeAppEngine(t)
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
				want = "codex://threads/" + testcodexipc.Thread + " codex://threads/new"
			}
			if strings.Join(got, " ") != want {
				t.Fatalf("restart navigated %q; only current non-archived delivery may navigate, want %q", got, want)
			}
			if scenario == "held" && len(app.Inputs()) != 1 {
				t.Fatalf("held mail input count=%d, want1", len(app.Inputs()))
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
