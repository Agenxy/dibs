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

func TestSocketEconomyStrictHookKeepsLossDiagnosticAtInfo(t *testing.T) {
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
	if text := log.String(); strings.Contains(text, "key=agent") {
		t.Fatalf("normal resolution diagnosis was logged as loss at INFO: %s", text)
	}
	if _, err = e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: r["token"].(string), To: "worker",
		MsgType: core.MsgQuestion, Body: "strict-unpresentable-mail",
	}); err != nil {
		t.Fatal(err)
	}
	e.SetWakePolicy(WakeNone)
	if _, err = e.HookPoll(ctx, sid, "Stop", "", false, true); err != nil {
		t.Fatal(err)
	}
	if text := log.String(); !strings.Contains(text, "key=queued") || strings.Contains(text, "key=agent") {
		t.Fatalf("actual unpresented-mail diagnostic lost its INFO severity: %s", text)
	}
}
