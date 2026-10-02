package harnessenv

import (
	"context"
	"errors"
	"testing"
)

func TestChatGPTHoldsBoundsItsOwnProcessProbes(t *testing.T) {
	for _, stuck := range []string{"/usr/bin/pgrep", "/usr/sbin/lsof"} {
		t.Run(stuck, func(t *testing.T) {
			original := appProbeOutput
			t.Cleanup(func() { appProbeOutput = original })
			timedOut := false
			appProbeOutput = func(ctx context.Context, binary string, _ ...string) ([]byte, error) {
				if binary != stuck {
					return []byte("101\n"), nil
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
				t.Fatalf("lost process evidence from completed pgrep: running=%v", running)
			}
		})
	}
}
