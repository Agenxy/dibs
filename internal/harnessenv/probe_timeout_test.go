// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestChatGPTHoldsBoundsItsOwnProcessProbes(t *testing.T) {
	for _, stuck := range []string{"inventory", "files"} {
		t.Run(stuck, func(t *testing.T) {
			original := appProbeOutput
			originalPass := appProcessPass
			t.Cleanup(func() { appProbeOutput = original; appProcessPass = originalPass })
			timedOut := false
			wait := func(ctx context.Context) error {
				if _, bounded := ctx.Deadline(); !bounded {
					t.Error("production API gave its app probe no deadline")
					return errors.New("unbounded probe")
				}
				<-ctx.Done()
				timedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
				return ctx.Err()
			}
			appProcessPass = func(ctx context.Context) (*processPass, error) {
				if stuck == "inventory" {
					return nil, wait(ctx)
				}
				return fixtureProcessPass(ctx, "101 Mon Oct 5 06:01:00 2026 /Applications/ChatGPT.app/Contents/Resources/codex-cli/bin/codex\n", false), nil
			}
			appProbeOutput = func(ctx context.Context, binary string, _ ...string) ([]byte, error) {
				if binary != "/usr/sbin/lsof" {
					t.Fatalf("unexpected command probe: %s", binary)
				}
				return nil, wait(ctx)
			}
			running, holds := ChatGPTHolds("fixture-thread")
			if !timedOut || holds {
				t.Fatalf("timed-out probe claimed thread ownership: running=%v holds=%v timeout=%v", running, holds, timedOut)
			}
			if running != (stuck == "files") {
				t.Fatalf("lost process evidence from completed inventory: running=%v", running)
			}
		})
	}
}

// A bundle can have dozens of helpers before its actual Codex runtime. The
// production probe must reach that runtime within the aggregate deadline.
func TestChatGPTHoldsFindsRuntimeBehindManyHelpers(t *testing.T) {
	original := appProbeOutput
	originalPass := appProcessPass
	t.Cleanup(func() { appProbeOutput = original; appProcessPass = originalPass })
	appProcessPass = func(ctx context.Context) (*processPass, error) {
		return fixtureProcessPass(ctx, "101 Mon Oct 5 06:01:00 2026 /Applications/ChatGPT.app/helper\n"+
			"102 Mon Oct 5 06:01:00 2026 /Applications/ChatGPT.app/helper\n"+
			"103 Mon Oct 5 06:01:00 2026 /Applications/ChatGPT.app/helper\n"+
			"999 Mon Oct 5 06:01:00 2026 /Applications/ChatGPT.app/Contents/Resources/codex-cli/bin/codex\n", false), nil
	}
	appProbeOutput = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
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
