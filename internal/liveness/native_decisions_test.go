// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package liveness

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/peerwake"
	"github.com/agenxy/dibs/internal/procexec"
	"github.com/agenxy/dibs/internal/procinfo"
)

func TestNativeDecisionsRetainDetachedOwnerAndRejectDeadOrReusedPID(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	input, release, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = release.Close() })
	parent := procexec.Command(ctx, os.Args[0], "-test.run=^TestNativeObservationProcessHelper$")
	parent.Env = append(os.Environ(), "DIBS_NATIVE_OBSERVATION_ROLE=parent", "DIBS_PARENT=detached-owner")
	parent.Stdin = input
	output, err := parent.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.Start(); err != nil {
		t.Fatal("setup parent:", err)
	}
	_ = input.Close()
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(output).ReadString('\n'); ready <- line }()
	var pid int
	select {
	case line := <-ready:
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "ready" {
			t.Fatalf("setup grandchild readiness: %q", line)
		}
		pid, err = strconv.Atoi(fields[1])
		if err != nil || pid <= 1 {
			t.Fatal("setup invalid child PID")
		}
	case <-ctx.Done():
		t.Fatal("setup grandchild never became ready")
	}
	started, err := procinfo.Start(pid)
	if err != nil {
		t.Fatal("setup child incarnation:", err)
	}
	t.Cleanup(func() {
		_ = release.Close()
		if current, err := procinfo.Start(pid); err == nil && current.Equal(started) {
			if p, err := os.FindProcess(pid); err == nil {
				_ = p.Kill()
			}
		}
	})
	if err := parent.Wait(); err != nil {
		t.Fatal("setup parent did not exit cleanly:", err)
	}
	if New().Alive(parent.Process.Pid) {
		t.Fatal("setup parent is still alive")
	}
	var mu sync.Mutex
	commands := map[string]int{}
	restore := procexec.ObserveCommands(func(name string) { mu.Lock(); commands[name]++; mu.Unlock() })
	defer restore()
	var observed *Agent
	for _, agent := range Discover() {
		if agent.PID == pid {
			row := agent
			observed = &row
		}
	}
	if observed == nil || observed.PPID == parent.Process.Pid || observed.Owner != "detached-owner" || observed.Via != "env" {
		t.Fatalf("departed parent lost the inherited owner: %+v", observed)
	}
	if peerwake.Alive(parent.Process.Pid, "a departed incarnation") {
		t.Fatal("dead parent remained a live peer")
	}
	if peerwake.Alive(pid, "an earlier incarnation of this PID") {
		t.Fatal("live PID accepted a mismatched start stamp")
	}
	if sample := Observe(pid, ""); !sample.Alive {
		t.Fatal("detached child was declared dead")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(commands) != 0 {
		t.Fatalf("native death/reuse/parent decisions prepared child processes: %v", commands)
	}
}

func TestNativeObservationProcessHelper(t *testing.T) {
	switch os.Getenv("DIBS_NATIVE_OBSERVATION_ROLE") {
	case "parent":
		child := procexec.Command(context.Background(), os.Args[0], "-test.run=^TestNativeObservationProcessHelper$")
		child.Args[0] = "claude"
		child.Env = append(os.Environ(), "DIBS_NATIVE_OBSERVATION_ROLE=child")
		child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			t.Fatal("setup spawn:", err)
		}
		// Deliberately leave a running child behind: the outer fixture owns
		// its stdin and checks this parent's actual exit before discovery.
		os.Exit(0)
	case "child":
		if _, err := io.WriteString(os.Stdout, fmt.Sprintf("ready %d\n", os.Getpid())); err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, os.Stdin)
	}
}
