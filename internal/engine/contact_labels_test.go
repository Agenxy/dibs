// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	hostdisplay "github.com/agenxy/dibs/internal/hostname"
)

// Register the actual metadata, mark the private fixture's process ended via
// a recorded sweep, then send through the writer. PendingForHuman is the real
// relay projection; the restart path also renders the sender's contact news.
func TestContactLabelsThroughRegisterSendAndRestart(t *testing.T) {
	const thread = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	for _, tc := range []struct {
		name, surface, session, app string
		remote                      bool
	}{
		{"local-missing-session", harnessenv.ChatGPTApp, "", "ChatGPT", false},
		{"remote-missing-session", harnessenv.ClaudeDesktop, "", "Claude", true},
		{"known-thread", harnessenv.ChatGPTApp, thread, "ChatGPT", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DIBS_DIR", t.TempDir())
			t.Setenv("CODEX_HOME", t.TempDir())
			t.Setenv("DIBS_TEST_FORBID_APP_OPEN", "1")
			friendly := hostdisplay.Name() // bounded lookup outside the writer
			kernel, err := os.Hostname()
			if err != nil {
				t.Fatal(err)
			}
			led := &memLedger{}
			e, ctx := runContactReplayEngine(t, core.NewState("contact-labels", core.DefaultLimits()), led, nil)
			hostID, label := e.HostID(), "obsolete-network-label"
			if tc.remote {
				hostID, label = strings.Repeat("a", 64), "ContactRemote"
			}
			asker := contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpRegister, Name: "asker", Nonce: "contact-label-asker"})
			registered := contactReplayDo(t, e, ctx, &core.Op{
				Kind: core.OpRegister, Name: "worker", Nonce: "contact-label-worker", AgentKind: core.KindPersistent, PID: 424242,
				SessionID: tc.session, Agent: &core.AgentInfo{Harness: "codex-dibs-recovery", Surface: tc.surface, HostID: hostID, Host: label},
			})
			if registered["token"] == nil {
				t.Fatal("setup: actual registration failed")
			}
			contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpSweep, DeadAgents: []string{"worker"}})
			setup, err := e.query(ctx, func() core.Result {
				l := e.state.Agents["worker"]
				return core.Result{
					"dormant": l.Status == core.StatusDormant, "route": e.hasWakeRoute(l),
					"native": e.nativeAppRoute(l), "surface": surfaceOf(l), "thread": threadIDOf(l),
				}
			})
			if err != nil || setup["dormant"] != true || setup["route"] != false || setup["native"] != false ||
				setup["surface"] != tc.surface || setup["thread"] != tc.session {
				t.Fatalf("setup did not establish the real registration with no delivery route: %v %v", setup, err)
			}
			feed, detach := e.AttachHumanRelay()
			t.Cleanup(detach)
			sent := contactReplayDo(t, e, ctx, &core.Op{
				Kind: core.OpSendMessage, Token: asker["token"].(string),
				To: "worker", MsgType: core.MsgRequest, Body: "PRIVATE-CONTACT-CONTENT",
			})
			if sent["msg_serial"] == nil {
				t.Fatal("setup: request did not send")
			}
			wait, cancel := context.WithTimeout(t.Context(), 3*contactSettleDelay+5*time.Second)
			defer cancel()
			select {
			case notice := <-feed:
				if notice.Type != "contact" || notice.Contact == nil {
					t.Fatalf("setup: unexpected relay notice: %+v", notice)
				}
			case <-wait.Done():
				t.Fatal("setup: real send never reached contact relay:", wait.Err())
			}
			pending, err := e.PendingForHuman(ctx)
			if err != nil || len(pending) != 1 || pending[0].Contact == nil {
				t.Fatalf("setup: missing real contact: %v %v", pending, err)
			}
			hint := pending[0].Contact.OpenHint
			if !strings.Contains(hint, tc.app) || strings.Contains(hint, "codex-dibs-recovery") || strings.Contains(hint, hostID) {
				t.Errorf("contact does not name the app and human host: %s", hint)
			}
			if tc.remote {
				if !strings.Contains(hint, "ContactRemote") {
					t.Errorf("remote host label missing: %s", hint)
				}
			} else if !strings.Contains(hint, friendly) && !strings.Contains(hint, kernel) && !strings.Contains(hint, hostdisplay.Name()) {
				t.Errorf("contact omitted the current display label or its honest kernel fallback: %s", hint)
			}
			if strings.Contains(hint, "PRIVATE-CONTACT-CONTENT") {
				t.Fatal("participant body leaked into contact")
			}
			if tc.session == "" {
				assertMissingContactSession(t, hint)
				if pending[0].Contact.OpenURL != "" {
					t.Error("missing thread produced an actionable window link")
				}
			} else if !strings.Contains(hint, "original session "+thread) || pending[0].Contact.OpenURL != "codex://threads/"+thread {
				t.Errorf("known original thread lost its hint/link: %+v", pending[0].Contact)
			}
			// A real cold engine rebuilds this warning from the actual ledger.
			restarted, restartCtx := replayContactEngineOnNode(t, e, ctx, led, "contact-labels")
			news := readContactUpdates(t, restarted, restartCtx, asker["token"].(string), "check_in")
			if tc.session == "" {
				assertMissingContactSession(t, news)
				if !strings.Contains(news, tc.app) || strings.Contains(news, "codex-dibs-recovery") || strings.Contains(news, hostID) {
					t.Errorf("cold sender notice lost app/host display: %s", news)
				}
			} else if !strings.Contains(news, "codex://threads/"+thread) {
				t.Errorf("cold known-thread notice lost its real window link: %s", news)
			}
		})
	}
}

func assertMissingContactSession(t *testing.T, text string) {
	t.Helper()
	for _, want := range []string{"no thread or session is known", "find or restart", "reassign"} {
		if !strings.Contains(strings.ToLower(text), want) {
			t.Errorf("missing-session contact omitted %q: %s", want, text)
		}
	}
	if strings.Contains(text, "original session ") || strings.Contains(text, "codex://threads/") {
		t.Errorf("contact invented a session or actionable window: %s", text)
	}
}
