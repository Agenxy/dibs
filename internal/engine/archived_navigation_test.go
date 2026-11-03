// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/testcodexipc"
	"github.com/agenxy/dibs/internal/testexec"
)

func TestArchivedChatGPTMailStillUsesNativeButNeverNavigates(t *testing.T) {
	for _, owned := range []bool{false, true} {
		t.Run(map[bool]string{false: "unloaded", true: "owned"}[owned], func(t *testing.T) {
			t.Setenv("DIBS_DIR", t.TempDir())
			app := testcodexipc.Start(t, "idle", nil)
			app.Owns = func(string) bool { return owned }
			var opens atomic.Int32
			oldShower := shower
			shower = &harnessenv.Shower{Ownership: func(string) harnessenv.ThreadOwnership {
				return harnessenv.ThreadOwnership{Running: true, Known: true, Loaded: owned}
			}, Open: func([]string) error { opens.Add(1); return nil }}
			t.Cleanup(func() { shower = oldShower })
			binary := filepath.Join(t.TempDir(), "codex")
			if b, err := exec.Command("go", "build", "-o", binary, "../wakeexec/testdata/codexqueue").CombinedOutput(); err != nil {
				t.Fatalf("fixture build: %v %s", err, b)
			}
			testexec.PrepareExecutable(t, exec.Command(binary, "--version"))
			e, ctx, _, sender := nativeAppEngine(t)
			e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{binary, "queue", "--thread", "{thread}", "--message", "{message}"}}})
			if _, err := e.query(ctx, func() core.Result { e.state.Agents["worker"].Status = core.StatusArchived; return nil }); err != nil {
				t.Fatal(err)
			}
			serial := nativeAppSend(t, e, ctx, sender)
			waitWakeDone(t, e, "worker")
			if opens.Load() != 0 {
				t.Fatalf("archived mail navigated %d times", opens.Load())
			}
			want := 0
			if owned {
				want = 1
			}
			if len(app.Inputs()) != want {
				t.Fatalf("native input=%d want%d", len(app.Inputs()), want)
			}
			r, err := e.query(ctx, func() core.Result { return core.Result{"stored": e.state.Messages[serial] != nil} })
			if err != nil || r["stored"] != true {
				t.Fatalf("archived mail lost: %v %v", r, err)
			}
			if !owned {
				nativeAppSend(t, e, ctx, sender)
				waitWakeDone(t, e, "worker")
				board, err := e.Board(ctx)
				if err != nil {
					t.Fatal(err)
				}
				alerts, ok := board["app_binding_alerts"].([]core.Result)
				if !ok || len(alerts) != 1 || opens.Load() != 0 {
					t.Fatalf("repeated mail duplicated binding reports or opened: %v opens=%d", alerts, opens.Load())
				}
			}
		})
	}
}

func TestArchivingDuringTheAppProbeCancelsTheActualOpen(t *testing.T) {
	t.Setenv("DIBS_DIR", t.TempDir())
	const thread = "0199a0b1-c2d3-4e5f-8a9b-0c1d2e3f4a5b"
	st := core.NewState("late-archive", core.DefaultLimits())
	if _, _, err := st.Apply(&core.Op{Kind: core.OpRegister, Name: "worker", SessionID: thread, NewToken: "fixture-worker"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	joined := make(chan struct{})
	go func() { e.Run(ctx); close(joined) }()
	t.Cleanup(func() { cancel(); <-joined })
	if _, err := e.query(ctx, func() core.Result { return nil }); err != nil {
		t.Fatal(err)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var opens atomic.Int32
	previous := shower
	shower = &harnessenv.Shower{Ownership: func(string) harnessenv.ThreadOwnership {
		close(entered)
		<-release
		return harnessenv.ThreadOwnership{Known: true, Running: true}
	}, Open: func([]string) error { opens.Add(1); return nil }}
	t.Cleanup(func() { shower = previous })
	plan := wakePlan{agent: "worker", thread: thread, createdSerial: st.Agents["worker"].CreatedSerial, surface: harnessenv.ChatGPTApp, harness: "Codex"}
	go func() { e.showInApp(plan, "worker"); close(done) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("setup: actual ownership probe not reached")
	}
	if _, err := e.query(ctx, func() core.Result { e.state.Agents["worker"].Status = core.StatusArchived; return nil }); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("actual open did not settle")
	}
	if opens.Load() != 0 {
		t.Fatal("agent archived during the ownership probe still navigated")
	}
}

func TestArchivedClaudeMailNeverReopensItsSession(t *testing.T) {
	f := newReopenContactFixture(t)
	if _, err := f.e.query(f.ctx, func() core.Result {
		a := f.e.state.Agents["lead"]
		a.Status = core.StatusArchived
		a.LastCoordination = time.Now().Add(-contactActivityGrace - time.Minute)
		delete(f.e.seen, "lead") // a truly archived recipient has no fresh turn evidence
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sent := f.do(t, &core.Op{Kind: core.OpSendMessage, Token: f.sender, To: "lead", MsgType: core.MsgRequest, Body: "stored archived request"})
	// Enter the actual writer-side closed-session door explicitly. The
	// peer-cache retry that can also reach it is asynchronous; observing the
	// board before that worker decides would test scheduling instead.
	if _, err := f.e.query(f.ctx, func() core.Result { f.e.openClosedSession(f.e.state.Agents["lead"]); return nil }); err != nil {
		t.Fatal(err)
	}
	// Wait on the actual decision, never an arbitrary quiet interval. The
	// dormant positive control uses this same fixture and does navigate.
	board, err := f.e.Board(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if alerts, ok := board["app_binding_alerts"].([]core.Result); !ok || len(alerts) != 1 {
		select {
		case argv := <-f.opened:
			t.Fatalf("archived Claude session navigated: %v", argv)
		case <-time.After(3 * time.Second):
			t.Fatal("archived navigation never settled into a binding diagnostic")
		}
	}
	serial := sent["msg_serial"].(uint64)
	r, err := f.e.query(f.ctx, func() core.Result { return core.Result{"stored": f.e.state.Messages[serial] != nil} })
	if err != nil || r["stored"] != true {
		t.Fatalf("archived request lost: %v %v", r, err)
	}
}
