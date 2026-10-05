package harnessenv

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestChatGPTHoldsBoundsItsOwnProcessProbes(t *testing.T) {
	for _, stuck := range []string{"/bin/ps", "/usr/sbin/lsof"} {
		t.Run(stuck, func(t *testing.T) {
			original := appProbeOutput
			t.Cleanup(func() { appProbeOutput = original })
			timedOut := false
			appProbeOutput = func(ctx context.Context, binary string, _ ...string) ([]byte, error) {
				if binary != stuck {
					return []byte("101 Mon Oct 5 06:01:00 2026 /Applications/ChatGPT.app/Contents/Resources/codex-cli/bin/codex\n"), nil
				}
				if _, bounded := ctx.Deadline(); !bounded {
					t.Error("production API gave its app probe no deadline")
					return nil, errors.New("unbounded probe")
				}
				<-ctx.Done()
				timedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
				return nil, ctx.Err()
			}
			running, holds := ChatGPTHolds("fixture-thread")
			if !timedOut || holds {
				t.Fatalf("timed-out probe claimed thread ownership: running=%v holds=%v timeout=%v", running, holds, timedOut)
			}
			if running != (stuck == "/usr/sbin/lsof") {
				t.Fatalf("lost process evidence from completed ps: running=%v", running)
			}
		})
	}
}

// A bundle can have dozens of helpers before its actual Codex runtime. The
// production probe must reach that runtime within the aggregate deadline.
func TestChatGPTHoldsFindsRuntimeBehindManyHelpers(t *testing.T) {
	original := appProbeOutput
	t.Cleanup(func() { appProbeOutput = original })
	appProbeOutput = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		if binary == "/bin/ps" || binary == "/usr/bin/pgrep" {
			return []byte("101 Mon Oct 5 06:01:00 2026 /Applications/ChatGPT.app/helper\n" +
				"102 Mon Oct 5 06:01:00 2026 /Applications/ChatGPT.app/helper\n" +
				"103 Mon Oct 5 06:01:00 2026 /Applications/ChatGPT.app/helper\n" +
				"999 Mon Oct 5 06:01:00 2026 /Applications/ChatGPT.app/Contents/Resources/codex-cli/bin/codex\n"), nil
		}
		if binary != "/usr/sbin/lsof" {
			t.Fatalf("unexpected probe %s", binary)
		}
		// A runtime behind helpers is visible to one runtime-only lsof query. Every
		// per-process query consumes the same budget that hid it on the live Mac.
		if strings.Join(args, " ") == "-a -p 999 -Fn" {
			return []byte("p999\nn/tmp/rollout-fixture-thread.jsonl\n"), nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(400 * time.Millisecond):
		}
		if len(args) > 1 && args[1] == "999" {
			return []byte("n/tmp/rollout-fixture-thread.jsonl\n"), nil
		}
		return []byte("n/helper-file\n"), nil
	}
	running, holds := ChatGPTHolds("fixture-thread")
	if !running || !holds {
		t.Fatalf("loaded runtime hidden behind helpers: running=%v holds=%v", running, holds)
	}
}
