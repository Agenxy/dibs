// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package liveness

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/agenxy/dibs/internal/procexec"
)

func TestNativeTranscriptDiscoveryFindsHeldFileWithoutChild(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".codex", "sessions", "2026", "11", "09", "rollout-native-fixture.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	var mu sync.Mutex
	commands := map[string]int{}
	restore := procexec.ObserveCommands(func(name string) { mu.Lock(); commands[name]++; mu.Unlock() })
	defer restore()
	if got := FindTranscript(os.Getpid()); got != path {
		t.Fatalf("production transcript lookup lost actual held file: %q want %q", got, path)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(commands) != 0 {
		t.Fatalf("transcript observation prepared children: %v", commands)
	}
}
