// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package liveness

import (
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/procexec"
)

// The backend's short-structure guard proves it returns an error. These tests
// start at the real consumer and prove that error reaches the existing command
// fallback, instead of becoming fabricated zero/no-files evidence.
func TestNativeCPUFailureUsesPSForThatObservation(t *testing.T) {
	old := nativeCPU
	t.Cleanup(func() { nativeCPU = old })
	calls := 0
	nativeCPU = func(int) (time.Duration, error) { calls++; return 0, errors.New("PIDTASKINFO returned95bytes, want96") }
	var mu sync.Mutex
	commands := map[string]int{}
	restore := procexec.ObserveCommands(func(name string) { mu.Lock(); commands[name]++; mu.Unlock() })
	defer restore()
	sample := Observe(os.Getpid(), "")
	if !sample.Alive || calls != 1 {
		t.Fatalf("did not reach native CPU through Observe: %+v calls=%d", sample, calls)
	}
	mu.Lock()
	defer mu.Unlock()
	if commands["ps"] != 1 {
		t.Fatalf("unavailable native CPU did not use existing ps fallback: %v", commands)
	}
}

func TestNativeDescriptorFailureUsesLSOFForThatObservation(t *testing.T) {
	old := nativeOpenFiles
	t.Cleanup(func() { nativeOpenFiles = old })
	calls := 0
	nativeOpenFiles = func(int, int) ([]string, error) {
		calls++
		return nil, errors.New("VNODEPATHINFO returned1199bytes, want1200")
	}
	var mu sync.Mutex
	commands := map[string]int{}
	restore := procexec.ObserveCommands(func(name string) { mu.Lock(); commands[name]++; mu.Unlock() })
	defer restore()
	_ = openFiles(os.Getpid())
	if calls != 1 {
		t.Fatalf("did not reach native descriptor edge: %d", calls)
	}
	mu.Lock()
	defer mu.Unlock()
	if commands["lsof"] != 1 {
		t.Fatalf("unavailable native descriptor evidence did not use existing lsof fallback: %v", commands)
	}
}
