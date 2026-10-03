package engine

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

// A queued notice is not evidence that a turn started. Drive the actual app
// deferral door and then read the real decorated board while it waits.
func TestBoardNamesAThreadWaitingForAwayOpening(t *testing.T) {
	original := shower
	t.Cleanup(func() { shower = original })
	var held atomic.Bool
	release := make(chan struct{})
	finished := make(chan struct{})
	shower = harnessenv.Shower{
		Holds:   func(string) bool { return held.Load() },
		Open:    func([]string) error { t.Error("opened while present"); return nil },
		Idle:    func() (time.Duration, bool) { return time.Second, true },
		MinIdle: 10 * time.Minute,
		Poll:    time.Millisecond,
		Wait:    func(time.Duration) { <-release; held.Store(true); close(finished) },
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := New(core.NewState("fixture", core.DefaultLimits()), &memLedger{}, deadProber{})
	go e.Run(ctx)
	const thread = "01876543-1234-4567-8901-234567890abc"
	reg, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "away-worker", SessionID: thread,
		Agent: &core.AgentInfo{Harness: "Codex", Surface: harnessenv.ChatGPTApp},
	})
	if err != nil {
		t.Fatal("setup:", err)
	}
	id := reg["agent_id"].(string)
	e.showInApp(wakePlan{thread: thread, harness: "Codex", surface: harnessenv.ChatGPTApp}, id)
	board, err := e.Board(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var status string
	for _, row := range board["agents"].([]map[string]any) {
		if row["id"] == id {
			status, _ = row["wake"].(string)
		}
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("deferred waiter did not finish")
	}
	if !strings.Contains(status, "queued") || !strings.Contains(status, "away") {
		t.Fatalf("board hid deferred delivery: wake=%q", status)
	}
}
