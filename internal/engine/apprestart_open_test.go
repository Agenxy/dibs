package engine

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/wakeexec"
)

// Enter at the restart sweep's post-admission path. If it stops handing the
// queued thread to the shared Shower, the fake native contact never sees it.
// The command and app contact are both fixtures: this must not touch a user's
// ChatGPT window or queue.
func TestRestartQueueOpensThroughSharedShower(t *testing.T) {
	t.Setenv("DIBS_DIR", t.TempDir())
	const thread = "0199a0b1-c2d3-4e5f-8a9b-0c1d2e3f4a5b"
	previousQueue, previousShower := restartQueue, shower
	t.Cleanup(func() { restartQueue, shower = previousQueue, previousShower })
	var admissions atomic.Int32
	restartQueue = func(argv []string, agent, dir string) wakeexec.RestartQueueOutcome {
		if agent != "worker" || dir == "" || len(argv) != 6 || argv[1] != "queue" ||
			argv[3] != thread || argv[5] != wakeexec.Compose(wakeexec.KindAppRestart) {
			return wakeexec.RestartQueueOutcome{}
		}
		admissions.Add(1)
		return wakeexec.RestartQueueOutcome{OK: true}
	}
	opened := make(chan []string, 1)
	var ownershipChecks atomic.Int32
	shower = &harnessenv.Shower{
		Holds: func(string) bool { ownershipChecks.Add(1); return false },
		Open:  func(argv []string) error { opened <- argv; return nil },
	}
	e := New(core.NewState("fixture", core.DefaultLimits()), &memLedger{}, deadProber{})
	plan := wakePlan{
		argv:   []string{"codex", "queue", "--thread", thread, "--message", wakeexec.Compose(wakeexec.KindAppRestart)},
		thread: thread, agent: "worker", cwd: t.TempDir(),
		surface: harnessenv.ChatGPTApp, harness: "Codex",
	}
	if !e.sendRestartQueue(context.Background(), "app:2", plan) || admissions.Load() != 1 {
		t.Fatalf("setup: restart queue admission failed or repeated: %d", admissions.Load())
	}
	select {
	case argv := <-opened:
		if ownershipChecks.Load() == 0 || len(argv) != 3 || argv[0] != "/usr/bin/open" ||
			argv[1] != "-g" || argv[2] != "codex://threads/"+thread {
			t.Fatalf("restart bypassed the shared app-open path: ownership=%d argv=%v", ownershipChecks.Load(), argv)
		}
	case <-time.After(12 * time.Second):
		t.Fatal("confirmed restart queue did not reach the shared app-open path")
	}
}
