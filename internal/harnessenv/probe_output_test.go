// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Cancellation must bound Output's pipe drain as well as the direct process.
// Start at the production command wrapper, not a Cmd with WaitDelay set by the
// test. The holder stays alive until cleanup, so its lifetime cannot make the
// assertion accidentally pass.
func TestAppProbeReturnsAfterCancellationWithInheritedOutput(t *testing.T) {
	dir := t.TempDir()
	ready, release := filepath.Join(dir, "ready"), filepath.Join(dir, "release")
	holderDone := filepath.Join(dir, "holder-done")
	t.Setenv("DIBS_TEST_PROBE_PIPE_MODE", "parent")
	t.Setenv("DIBS_TEST_PROBE_PIPE_READY", ready)
	t.Setenv("DIBS_TEST_PROBE_PIPE_RELEASE", release)
	t.Setenv("DIBS_TEST_PROBE_PIPE_DONE", holderDone)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type result struct{ err error }
	done := make(chan result, 1)
	go func() {
		_, err := appProbeOutput(ctx, os.Args[0], "-test.run=^TestAppProbeHeldPipeHelper$")
		done <- result{err}
	}()
	var returned *result
	defer func() {
		cancel()
		if err := os.WriteFile(release, []byte("release"), 0o600); err != nil {
			t.Error("fixture release:", err)
		}
		if returned == nil {
			select {
			case r := <-done:
				returned = &r
			case <-time.After(10 * time.Second):
				t.Error("fixture cleanup: command did not return after releasing inherited output")
			}
		}
		// Do not let TempDir remove the release before the holder sees it.
		cleanup := time.NewTimer(10 * time.Second)
		defer cleanup.Stop()
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			if _, err := os.Stat(holderDone); err == nil {
				return
			}
			select {
			case <-cleanup.C:
				t.Error("fixture cleanup: inherited-output holder did not close its pipes")
				return
			case <-tick.C:
			}
		}
	}()
	setup := time.NewTimer(10 * time.Second)
	defer setup.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case r := <-done:
			returned = &r
			t.Fatalf("setup: command exited before its descendant held stdout: %v", r.err)
		case <-setup.C:
			t.Fatal("setup: descendant never proved it held stdout")
		case <-tick.C:
		}
	}
	canceledAt := time.Now()
	cancel()
	select {
	case r := <-done:
		returned = &r
		t.Logf("context cancelled at %s; Output returned after %s", canceledAt.UTC().Format(time.RFC3339Nano), time.Since(canceledAt))
		if r.err == nil {
			t.Fatal("cancelled probe reported successful partial output")
		}
	case <-time.After(time.Second):
		t.Error("probe Output outlived cancellation by more than 1s while a descendant held stdout")
	}
}

func TestAppProbeHeldPipeHelper(t *testing.T) {
	mode := os.Getenv("DIBS_TEST_PROBE_PIPE_MODE")
	if mode == "" {
		return
	}
	if mode == "parent" {
		child := exec.Command(os.Args[0], "-test.run=^TestAppProbeHeldPipeHelper$")
		child.Env = append(os.Environ(), "DIBS_TEST_PROBE_PIPE_MODE=holder")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			t.Fatal("fixture spawn:", err)
		}
		// The parent is killed by CommandContext after the holder's setup
		// receipt. This cap also ends it if its caller crashes during setup.
		<-time.After(time.Minute)
		_ = child.Process.Kill()
		_ = child.Wait()
		return
	}
	if mode != "holder" {
		t.Fatalf("unknown fixture mode %q", mode)
	}
	defer func() {
		_ = os.Stdout.Close()
		_ = os.Stderr.Close()
		_ = os.WriteFile(os.Getenv("DIBS_TEST_PROBE_PIPE_DONE"), []byte("closed"), 0o600)
		if t.Failed() {
			os.Exit(1)
		}
		os.Exit(0)
	}()
	if _, err := fmt.Fprintln(os.Stdout, "inherited-output-held"); err != nil {
		t.Fatal("fixture output:", err)
	}
	if err := os.WriteFile(os.Getenv("DIBS_TEST_PROBE_PIPE_READY"), []byte("ready"), 0o600); err != nil {
		t.Fatal("fixture receipt:", err)
	}
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	cap := time.NewTimer(time.Minute)
	defer cap.Stop()
	for {
		select {
		case <-tick.C:
			if _, err := os.Stat(os.Getenv("DIBS_TEST_PROBE_PIPE_RELEASE")); err == nil {
				return
			}
		case <-cap.C:
			return
		}
	}
}
