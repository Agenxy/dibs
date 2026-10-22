// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin

package paths

import (
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// 2026-10-22: the launchd-run daemon's open(2) of ~/Desktop paths stopped
// returning, and every hook_poll and declare from an agent working there
// hung behind it (375 goroutines in one dump). A blocked open must cost a
// caller at most the budget, and repeated calls must not start more probes.
func TestNativeSpellingDoesNotWaitOnABlockedOpen(t *testing.T) {
	release := make(chan struct{})
	var opens atomic.Int32
	previous := openForSpelling
	openForSpelling = func(p string) (*os.File, error) {
		opens.Add(1)
		<-release
		return nil, os.ErrPermission
	}
	t.Cleanup(func() { openForSpelling = previous })

	const path = "/blocked/by/the/kernel"
	answered := make(chan time.Duration, 3)
	for range 3 {
		go func() {
			started := time.Now()
			if got := nativeSpelling(path); got != path {
				t.Errorf("a blocked probe must keep the name given, got %q", got)
			}
			answered <- time.Since(started)
		}()
	}
	deadline := time.After(5 * time.Second)
	for range 3 {
		select {
		case took := <-answered:
			if took > 2*spellingBudget {
				t.Errorf("a caller waited %s on a blocked open; the budget is %s", took, spellingBudget)
			}
		case <-deadline:
			t.Fatal("nativeSpelling is still waiting on an open that never returns")
		}
	}
	if n := opens.Load(); n != 1 {
		t.Fatalf("%d probes of one path were started while the first was blocked; want 1", n)
	}

	close(release)
	waitUntil := time.Now().Add(2 * time.Second)
	for {
		spellingProbes.Lock()
		_, running := spellingProbes.running[path]
		spellingProbes.Unlock()
		if !running {
			break
		}
		if time.Now().After(waitUntil) {
			t.Fatal("the probe was not retired after its open returned")
		}
		<-time.After(5 * time.Millisecond)
	}
	opens.Store(0)
	if got := nativeSpelling(path); got != path || opens.Load() != 1 {
		t.Fatalf("after the open returned, a new call should probe again: got %q, %d opens", got, opens.Load())
	}
}

// A real directory still answers with its native spelling.
func TestNativeSpellingStillAnswersForARealPath(t *testing.T) {
	dir := t.TempDir()
	if got := nativeSpelling(dir); got == "" {
		t.Fatal("no spelling for a real directory")
	}
}
