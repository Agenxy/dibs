// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package liveness

import (
	"context"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/procexec"
	"github.com/agenxy/dibs/internal/procinfo"
)

func TestEmptyNativeEnvironmentCannotChooseAHeuristicOwner(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	child := procexec.Command(ctx, os.Args[0], "-test.run=^TestEmptyNativeEnvironmentHelper$", "-test.timeout=90s")
	child.Args[0] = "claude"
	child.Env = []string{}
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
		t.Fatalf("setup: empty-env child readiness %q: %v", signal, err)
	}
	env, err := procinfo.Environ(child.Process.Pid)
	if len(env) != 0 {
		t.Fatalf("setup: empty environment contained %d entries", len(env))
	}
	if err == nil || procinfo.Gone(err) {
		t.Errorf("live empty environment must be unavailable, not known empty or gone: %v", err)
	}
	var mu sync.Mutex
	var commands []string
	restore := procexec.ObserveCommands(func(command string) { mu.Lock(); commands = append(commands, command); mu.Unlock() })
	defer restore()
	owner, via := attributeIn(child.Process.Pid, 123, map[int]string{123: "claude"})
	if owner != "" || via != "unknown" {
		t.Errorf("unavailable environment chose a weaker owner: owner=%q via=%q", owner, via)
	}
	found := false
	for _, agent := range Discover() {
		if agent.PID != child.Process.Pid {
			continue
		}
		found = true
		if agent.Owner != "" || agent.Via != "unknown" {
			t.Errorf("discovery lost unknown attribution: %+v", agent)
		}
	}
	if !found {
		t.Error("unknown child disappeared from process discovery")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(commands) != 0 {
		t.Errorf("unavailable environment started a command to reread the same evidence: %v", commands)
	}
}

func TestEmptyNativeEnvironmentHelper(t *testing.T) {
	if len(os.Environ()) != 0 {
		t.Skip("not the empty-environment child")
	}
	if _, err := io.WriteString(os.Stdout, "R"); err != nil {
		t.Fatal(err)
	}
	select {}
}
