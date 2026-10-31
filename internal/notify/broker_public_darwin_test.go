// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package notify

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/testexec"
)

// The public adapter discovers an installed helper beside this copied driver.
// Two actual calls share one child. Responses are demultiplexed by board ID;
// death fails the waiting ask, and the next call respawns without replay.
func TestBrokerRoutesConcurrentCallsAndRecoversAfterDeath(t *testing.T) {
	if os.Getenv("DIBS_TEST_BROKER_DRIVER") != "1" {
		dir := t.TempDir()
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		image, err := os.ReadFile(self)
		if err != nil {
			t.Fatal(err)
		}
		driver, helper := filepath.Join(dir, "driver.test"), filepath.Join(dir, helperName)
		if err := os.MkdirAll(filepath.Dir(helper), 0o700); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{driver, helper} {
			if err := os.WriteFile(path, image, 0o700); err != nil {
				t.Fatal(err)
			}
			testexec.PrepareExecutable(t, exec.Command(path, "--dibs-test-prepare"))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, driver, "-test.run=^TestBrokerRoutesConcurrentCallsAndRecoversAfterDeath$")
		cmd.Env = append(os.Environ(), "DIBS_NOTIFY=", "DIBS_TEST_RECEIPT_PUBLIC=1", "DIBS_TEST_BROKER_DRIVER=1", "DIBS_TEST_BROKER_PID_CAPTURE="+filepath.Join(dir, "pids"), "DIBS_DIR="+dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("real broker adapter: %v\n%s", err, out)
		}
		return
	}
	type answer struct {
		choice string
		err    error
	}
	a, b := make(chan answer, 1), make(chan answer, 1)
	postedA, postedB := make(chan struct{}, 1), make(chan struct{}, 1)
	ask := func(serial uint64, title string, posted chan struct{}, out chan answer) {
		choice, err := AskMessage("broker-public", serial, title, "excerpt", func(state string) {
			if state == "posted" {
				posted <- struct{}{}
			}
		}, "Deny", "Approve")
		out <- answer{choice, err}
	}
	go ask(7, "concurrent A", postedA, a)
	select {
	case <-postedA:
	case result := <-a:
		t.Fatalf("A never posted: %+v", result)
	case <-time.After(3 * time.Second):
		t.Fatal("A setup timed out")
	}
	go ask(8, "concurrent B", postedB, b)
	select {
	case <-postedB:
	case result := <-b:
		t.Fatalf("B never posted: %+v", result)
	case <-time.After(3 * time.Second):
		t.Fatal("B setup timed out")
	}
	select {
	case result := <-a:
		if result.err != nil || result.choice != "Approve" {
			t.Fatalf("A response misrouted: %+v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("A response lost")
	}
	select {
	case result := <-b:
		t.Fatalf("A response also answered B: %+v", result)
	default:
	}
	cancelNativeAsks([]string{"dibs.msg.broker-public.8"})
	select {
	case result := <-b:
		if result.err != nil || result.choice != "" {
			t.Fatalf("B cancellation fabricated a verdict: %+v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("B unaffected/cancellable route lost")
	}
	posted := false
	choice, err := AskWithReceipt("die", "excerpt", func(state string) { posted = state == "posted" }, "Approve")
	if !posted || choice != "" || !errors.Is(err, ErrNoAnswer) {
		t.Fatalf("broker death fabricated dismissal: posted=%t choice=%q err=%v", posted, choice, err)
	}
	choice, err = Ask("unoffered", "excerpt", "Approve")
	if err != nil || choice != "Approve" {
		t.Fatalf("respawn or action validation failed: %q %v", choice, err)
	}
	raw, err := os.ReadFile(os.Getenv("DIBS_TEST_BROKER_PID_CAPTURE"))
	if err != nil {
		t.Fatal(err)
	}
	pids := strings.Fields(string(raw))
	if len(pids) != 2 || pids[0] == pids[1] {
		t.Fatalf("expected one shared broker and one event-driven respawn, got %v", pids)
	}
}
