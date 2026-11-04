// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"io"
	"net"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/procexec"
	"github.com/agenxy/dibs/internal/testexec"
)

func TestAppServerReadStopsWhenContextEndsWithRetainedStdout(t *testing.T) {
	testAppServerRetainedStdout(t, false, "initialize")
}

func TestAppServerDeadlineStopsWithOnlyStdoutRetained(t *testing.T) {
	for _, phase := range []string{"initialize", "hooks/list"} {
		t.Run(phase, func(t *testing.T) { testAppServerRetainedStdout(t, true, phase) })
	}
}

func testAppServerRetainedStdout(t *testing.T, stdoutOnly bool, phase string) {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "codex")
	if out, err := procexec.Command(t.Context(), "go", "build", "-o", binary, "./testdata/codexplugin").CombinedOutput(); err != nil {
		t.Fatalf("setup: fixture build: %s %v", out, err)
	}
	testexec.PrepareExecutable(t, exec.Command(binary, "--version"))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	if err := ln.(*net.TCPListener).SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIBS_TEST_HOOK_PIPE_CONTROL", ln.Addr().String())
	t.Setenv("DIBS_TEST_HOOK_PIPE_PHASE", phase)
	var ctx context.Context
	var cancel context.CancelFunc
	if stdoutOnly {
		t.Setenv("DIBS_TEST_HOOK_STDOUT_ONLY", "1")
		ctx, cancel = context.WithTimeout(t.Context(), 2*time.Second)
	} else {
		ctx, cancel = context.WithCancel(t.Context())
	}
	defer cancel()
	done := make(chan error, 1)
	home := t.TempDir()
	go func() {
		server, err := startAppServerForHome(ctx, binary, home)
		if err == nil && phase == "hooks/list" {
			_, err = server.dibsHooks()
		}
		if server != nil {
			server.close()
		}
		done <- err
	}()
	controls := map[byte]net.Conn{}
	waited := false
	t.Cleanup(func() {
		cancel()
		for role, control := range controls {
			_ = control.SetDeadline(time.Now().Add(5 * time.Second))
			if role == 'g' {
				if _, err := control.Write([]byte{'x'}); err != nil {
					t.Errorf("release grandchild: %v", err)
				}
			} else {
				_, _ = control.Write([]byte{'e'})
			}
			if extra, err := io.ReadAll(control); err != nil || len(extra) != 0 {
				t.Errorf("%c exit receipt: %q %v", role, extra, err)
			}
			_ = control.Close()
		}
		if !waited {
			select {
			case err := <-done:
				t.Logf("app-server returned after fixture release: %v", err)
			case <-time.After(5 * time.Second):
				t.Error("app-server fixture did not finish after release")
			}
		}
	})
	for range 2 {
		control, err := ln.Accept()
		if err != nil {
			t.Fatal("fixture did not connect:", err)
		}
		_ = control.SetReadDeadline(time.Now().Add(5 * time.Second))
		var role [1]byte
		if _, err := io.ReadFull(control, role[:]); err != nil || (role[0] != 'p' && role[0] != 'g') || controls[role[0]] != nil {
			_ = control.Close()
			t.Fatalf("fixture role: %q %v", role, err)
		}
		controls[role[0]] = control
	}
	parent := controls['p']
	if _, err := parent.Write([]byte{'e'}); err != nil {
		t.Fatal(err)
	}
	if extra, err := io.ReadAll(parent); err != nil || len(extra) != 0 {
		t.Fatalf("setup: direct child did not exit before cancellation: %q %v", extra, err)
	}
	_ = parent.Close()
	delete(controls, 'p')
	started := time.Now()
	deadline := started
	if stdoutOnly {
		deadline, _ = ctx.Deadline()
		if ctx.Err() != nil {
			t.Fatal("setup: fixture was not ready before the probe deadline")
		}
	} else {
		cancel()
	}
	select {
	case err := <-done:
		waited = true
		if err == nil {
			t.Fatal("incomplete initialize reply became an app-server session")
		}
		t.Logf("app-server returned %s after fixture ready; deadline was %s later: %v", time.Since(started), deadline.Sub(started), err)
	case <-time.After(time.Until(deadline) + 3*time.Second):
		t.Fatal("manual app-server read outlived its context deadline and pipe-drain allowance")
	}
}
