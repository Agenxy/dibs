// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package engine

import (
	"bufio"
	"context"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/liveness"
	"github.com/agenxy/dibs/internal/peerwake"
	"github.com/agenxy/dibs/internal/procexec"
)

// Start at the real observing doors, with a live subagent fixture. Replacing
// an internal native reader by hand would miss a reader that is never wired.
// The constructor guard makes procexec the sole production child-process door.
func TestIdleProcessObservationStartsNoChildren(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	child := procexec.Command(ctx, os.Args[0], "-test.run=^TestIdleProcessChildHelper$")
	child.Args[0] = "claude"
	child.Env = append(os.Environ(), "DIBS_TEST_IDLE_PROCESS_CHILD=1", "DIBS_PARENT=idle-observation-fixture")
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal("fixture start:", err)
	}
	t.Cleanup(func() { _ = input.Close(); cancel(); _ = child.Wait() })
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(output).ReadString('\n'); ready <- line }()
	select {
	case line := <-ready:
		if line != "ready\n" {
			t.Fatalf("fixture readiness: %q", line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("fixture never became ready")
	}
	pid := child.Process.Pid
	stamp := harnessenv.ProcessStart(pid)
	if stamp == "" {
		t.Fatal("setup: child start unavailable")
	}
	// Socket sidecars use ps's padded day, whereas bridge claims normalize
	// whitespace. Obtain each contract independently before counting children.
	peerStart := procexec.Command(ctx, "ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	peerStart.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")
	peerRaw, err := peerStart.Output()
	if err != nil || strings.TrimSpace(string(peerRaw)) == "" {
		t.Fatalf("setup: sidecar start unavailable: %v", err)
	}
	peerStamp := strings.TrimSpace(string(peerRaw))
	e := New(core.NewState("idle-observation", core.DefaultLimits()), &memLedger{}, liveness.New())
	e.SetSocketWakes(false)
	e.SetNoticesWake(false)
	go e.Run(ctx)
	if _, err := e.query(ctx, func() core.Result { return core.Result{} }); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); <-e.Done() }()
	var mu sync.Mutex
	commands := map[string]int{}
	restore := procexec.ObserveCommands(func(name string) { mu.Lock(); commands[name]++; mu.Unlock() })
	defer restore()
	for range 2 {
		if _, err := e.query(ctx, func() core.Result {
			now := time.Now()
			e.sweep(now)
			e.expireWaiters(now)
			e.pruneSocketState()
			return core.Result{}
		}); err != nil {
			t.Fatal(err)
		}
		if epoch, known := harnessenv.ChatGPTAppEpoch(); !known || epoch == "" {
			t.Fatal("actual app inventory unavailable")
		}
		observed, err := harnessenv.AppForBridges(ctx, []harnessenv.BridgeIncarnation{{PID: pid, Start: stamp}})
		if err != nil || len(observed) != 1 || !observed[0].Present {
			t.Fatalf("live bridge disappeared from actual inventory: %+v %v", observed, err)
		}
		if !peerwake.Alive(pid, peerStamp) {
			t.Fatal("live child lost its peer identity")
		}
		sample := liveness.Observe(pid, "")
		if !sample.Alive {
			t.Fatalf("live child not sampled: %+v", sample)
		}
		_ = liveness.FindTranscript(pid)
		found := false
		for _, agent := range liveness.Discover() {
			if agent.PID == pid {
				found = agent.Owner == "idle-observation-fixture"
			}
		}
		if !found {
			t.Fatal("setup: discovery did not attribute the real live fixture")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(commands) != 0 {
		t.Fatalf("idle observation prepared child processes: %v", commands)
	}
}

func TestIdleProcessChildHelper(t *testing.T) {
	if os.Getenv("DIBS_TEST_IDLE_PROCESS_CHILD") != "1" {
		return
	}
	if _, err := io.WriteString(os.Stdout, "ready\n"); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}
