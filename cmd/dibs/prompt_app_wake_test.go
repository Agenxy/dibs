package main

import (
	"context"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/harnessenv"
)

// The remote producer enters at serve, including actual queue outcome
// reporting. A fresh bridge instance still shares the per-thread memo.
func TestBridgePromptAppWakeIsImmediateAndBoundedAcrossProducers(t *testing.T) {
	t.Setenv("DIBS_DIR", t.TempDir())
	opens := 0
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	for i := uint64(1); i <= 8; i++ {
		recorder := &recordingRun{ok: true}
		b := bridgeUnderTest(t, recorder)
		bridgeReportServer(t, b)
		b.show = harnessenv.Shower{
			Holds: func(string) bool { return false },
			Open: func(argv []string) error {
				if len(argv) != 3 || argv[1] != "-g" {
					t.Errorf("not background open: %q", argv)
				}
				opens++
				return nil
			},
			Away:    func() (bool, bool) { return false, true },
			Idle:    func() (time.Duration, bool) { return 0, true },
			MinIdle: 10 * time.Minute,
			Wait:    func(time.Duration) { <-release },
		}
		b.serve(context.Background(), engine.WakeRequest{
			ID: i, Host: b.host, Agent: "worker", Harness: "codex", Thread: "bridge-prompt-fixture",
			Surface: harnessenv.ChatGPTApp, MsgType: "question",
		})
		if len(recorder.runs) != 1 {
			t.Fatalf("setup: bridge did not execute the successful queue route: %q", recorder.runs)
		}
	}
	if opens != 1 {
		t.Fatalf("eight producers opened one dormant thread %d times, want one", opens)
	}
}
