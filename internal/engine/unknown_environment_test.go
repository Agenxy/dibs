// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package engine

import (
	"context"
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/liveness"
	"github.com/agenxy/dibs/internal/testexec"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestUnknownEnvironmentCannotFeedSupervision(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "claude")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./testdata/emptyenv")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("setup: empty-env fixture build: %v: %s", err, out)
	}
	testexec.PrepareExecutable(t, exec.Command(binary, "--version"))
	input, release, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close(); _ = release.Close() }()
	child := exec.CommandContext(ctx, binary)
	child.Env = []string{}
	child.Stdin = input
	ready, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	var signal [1]byte
	if _, err := io.ReadFull(ready, signal[:]); err != nil || signal[0] != 'R' {
		t.Fatalf("setup: child readiness %q: %v", signal, err)
	}
	found := false
	for _, agent := range liveness.Discover() {
		if agent.PID == child.Process.Pid {
			found = true
		}
	}
	if !found {
		t.Fatal("setup: live empty-env harness absent from discovery")
	}
	engine := New(core.NewState("test", core.DefaultLimits()), &memLedger{}, deadProber{})
	engineCtx := runCapacityEngine(t, engine)
	pid := child.Process.Pid
	history := map[int][]liveness.Sample{pid: {{}}}
	told := map[int]bool{pid: true}
	engine.superviseOnce(engineCtx, liveness.DefaultConfig(), history, told)
	if _, ok := history[pid]; ok {
		t.Error("unknown ownership entered supervision history")
	}
	if _, ok := told[pid]; ok {
		t.Error("unknown ownership retained notice suppression")
	}
}
