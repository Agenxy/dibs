// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/wakeexec"
)

// The complete production entry: Engine.Run observes two stable app epochs,
// selects a recently active ChatGPT-app agent, admits one queue item, and
// gives that agent its declaration snapshot on authenticated check_in.
// Only the external process observation, queue command, and app contact are
// fixtures; a Go test must never restart or open the operator's real app.
func TestAppRestartEpochQueuesRecentThreadAndReportsThroughCheckIn(t *testing.T) {
	const thread = "0199a0b1-c2d3-4e5f-8a9b-0c1d2e3f4a5b"
	const declaration = "finish the restart proof"
	t.Setenv("DIBS_DIR", t.TempDir())
	previousEpoch, previousQueue, previousShower := appRestartEpoch.Load(), restartQueue, shower
	var epoch atomic.Value
	epoch.Store("app:A")
	var probeCalls atomic.Int32
	baselineObserved := make(chan struct{}, 1)
	appRestartEpoch.Store(func() (string, bool) {
		if probeCalls.Add(1) == 4 {
			baselineObserved <- struct{}{}
		}
		return epoch.Load().(string), true
	})
	queued := make(chan string, 1)
	restartQueue = func(argv []string, agent, dir string) wakeexec.RestartQueueOutcome {
		if agent != "hand" || dir == "" || len(argv) != 6 || argv[0] != "codex" ||
			argv[1] != "queue" || argv[3] != thread || !strings.Contains(argv[5], "app restarted") {
			return wakeexec.RestartQueueOutcome{}
		}
		select {
		case queued <- argv[5]:
		default:
		}
		return wakeexec.RestartQueueOutcome{OK: true}
	}
	shown := make(chan struct{}, 1)
	shower = &harnessenv.Shower{
		Holds: func(string) bool { // loaded: no native open is needed
			select {
			case shown <- struct{}{}:
			default:
			}
			return true
		},
		Open: func([]string) error { panic("restart proof contacted the real app") },
	}
	st := core.NewState("restart-proof", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	e.SetWakeCommands(map[string]WakeCommand{"codex": {
		Argv: []string{"codex", "queue", "--thread", "{thread}", "--message", "{message}"},
	}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
		appRestartEpoch.Store(previousEpoch)
		restartQueue, shower = previousQueue, previousShower
	})
	registered, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "hand", AgentKind: core.KindPersistent, Nonce: "headline-proof-nonce",
	})
	if err != nil {
		t.Fatal("setup registration:", err)
	}
	token, _ := registered["token"].(string)
	if token == "" {
		t.Fatalf("setup issued no token: %v", registered)
	}
	workDir := t.TempDir()
	onLoop(t, ctx, e, func(state *core.State) {
		l := state.Agents["hand"]
		l.Role = core.RoleCoordinator
		l.Agent = &core.AgentInfo{Harness: "codex", Surface: harnessenv.ChatGPTApp, HostID: state.NodeID, CWD: workDir}
		l.CurrentSession = thread
		l.LastCoordination = time.Now().UTC().Add(-5 * time.Minute)
		l.Slots = map[string]core.Slot{"s1": {ID: "s1", Text: declaration, UpdatedSerial: state.Serial}}
	})
	_, settingErr := e.Configure(ctx, token, "wake.resume_after_app_restart", "1h")
	// Four watcher samples establish A and capture the agent while the old
	// app is still running. The old-source proof has no watcher; the bounded
	// fallback still changes its simulated epoch and checks the two absences.
	select {
	case <-baselineObserved:
	case <-time.After(9 * time.Second):
	}
	epoch.Store("app:B")
	var item string
	select {
	case item = <-queued:
	case <-time.After(12 * time.Second):
	}
	if item != "" {
		select {
		case <-shown:
		case <-time.After(2 * time.Second):
			t.Fatal("queued restart bypassed the shared app contact")
		}
	}
	checkIn, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: token})
	if err != nil {
		t.Fatal("authenticated check_in:", err)
	}
	updates, _ := checkIn["agent_updates"].([]string)
	notice := strings.Join(updates, "\n")
	if settingErr != nil || item == "" || !strings.Contains(notice, declaration) ||
		!strings.Contains(notice, "ChatGPT app restart") {
		t.Fatalf("app epoch change did not queue and report recent work: setting=%v probes=%d queue=%q notice=%q",
			settingErr, probeCalls.Load(), item, notice)
	}
	if strings.Contains(item, declaration) || strings.Contains(item, "hand") {
		t.Fatalf("queue argv leaked a declaration or agent name: %q", item)
	}
}
