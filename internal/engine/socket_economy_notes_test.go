// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

type economyLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

type economyDeadProcess struct{}

func (economyDeadProcess) Alive(int) bool { return false }

func (b *economyLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *economyLog) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func newEconomyNoteEngine(t *testing.T, prober Prober) (*Engine, context.Context) {
	t.Helper()
	e := New(core.NewState("notes", core.DefaultLimits()), &memLedger{}, prober)
	ctx, cancel := context.WithCancel(context.Background())
	joined := make(chan struct{})
	go func() { e.Run(ctx); close(joined) }()
	stopWakeTimersOnCleanup(t, e)
	t.Cleanup(func() { cancel(); <-joined })
	return e, ctx
}

func TestSocketEconomyClosedHarnessNoteUsesProcessEvidence(t *testing.T) {
	for _, mode := range []string{"dead", "live", "unprobed", "dormant without PID"} {
		t.Run(mode, func(t *testing.T) {
			var prober Prober = economyDeadProcess{}
			pid := 4242
			switch mode {
			case "live":
				prober = aliveProber{}
			case "unprobed":
				prober = nil
			case "dormant without PID":
				pid = 0
			}
			e, ctx := newEconomyNoteEngine(t, prober)
			for _, name := range []string{"worker", "sender"} {
				n := 0
				if name == "worker" {
					n = pid
				}
				if _, err := e.Do(ctx, &core.Op{
					Kind: core.OpRegister, Name: name, PID: n,
					AgentKind: core.KindPersistent, Nonce: "nonce-" + name,
				}); err != nil {
					t.Fatal("setup:", err)
				}
			}
			if mode == "dormant without PID" {
				if _, err := e.Do(ctx, &core.Op{Kind: core.OpSweep, StaleAgents: []string{"worker"}}); err != nil {
					t.Fatal("setup: record dormancy:", err)
				}
			}
			r, err := e.query(ctx, func() core.Result {
				return core.Result{"token": e.state.Agents["sender"].Token}
			})
			if err != nil {
				t.Fatal(err)
			}
			sent, err := e.Do(ctx, &core.Op{
				Kind: core.OpSendMessage, Token: r["token"].(string), To: "worker",
				MsgType: core.MsgQuestion, Body: "closed-harness-question",
			})
			if err != nil {
				t.Fatal("setup: real send:", err)
			}
			// The MCP send handler appends this same exported live-route
			// projection after the successful send, outside the fold.
			text := fmtResult(sent) + e.PullOnlyNoteFor(ctx, "worker")
			if got := strings.Contains(text, "harness closed; delivered on its next start"); got != (mode == "dead") {
				t.Fatalf("closure report did not follow actual process evidence (%s): %s", mode, text)
			}
		})
	}
}

// WakeNone deliberately holds mail for a natural activation. The old guard
// required an INFO loss alarm for that choice, although the question was still
// present. Unknown unsupported output has its separate INFO/redaction guard.
func TestSocketEconomyStrictHookKeepsDisabledWakeMailForNextActivation(t *testing.T) {
	var log economyLog
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	e, ctx := newEconomyNoteEngine(t, nil)
	const sid = "strict-economy-session"
	r, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "worker", SessionID: sid})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.HookPoll(ctx, sid, "Stop", "", true, true); err != nil {
		t.Fatal(err)
	}
	const marker = "strict-unpresentable-mail"
	if _, err = e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: r["token"].(string), To: "worker",
		MsgType: core.MsgQuestion, Body: marker,
	}); err != nil {
		t.Fatal(err)
	}
	e.SetWakePolicy(WakeNone)
	held, err := e.HookPoll(ctx, sid, "Stop", "", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if held["decision"] != nil || held["reason"] != nil || held["hookSpecificOutput"] != nil {
		t.Fatalf("disabled wake unexpectedly carried model context: %v", held)
	}
	for field := range held {
		switch field {
		case "continue", "stopReason", "suppressOutput", "systemMessage":
		default:
			t.Fatalf("held Stop contains a field outside its strict schema: %q", field)
		}
	}
	delivered, err := e.HookPoll(ctx, sid, "SessionStart", "", false, true)
	if err != nil {
		t.Fatal(err)
	}
	carrier, _ := delivered["hookSpecificOutput"].(map[string]any)
	digest, _ := carrier["additionalContext"].(string)
	if carrier["hookEventName"] != "SessionStart" || !strings.Contains(digest, marker) {
		t.Fatalf("deliberately held mail was lost before the next natural activation: %v", delivered)
	}
	again, err := e.HookPoll(ctx, sid, "SessionStart", "", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if again["hookSpecificOutput"] != nil || again["reason"] != nil || again["decision"] != nil {
		t.Fatalf("the held question was presented twice: %v", again)
	}
	if text := log.String(); strings.Contains(text, "dropped from a strict hook response") {
		t.Fatalf("successful intentional deferral was reported as lost information at INFO: %s", text)
	}
}
