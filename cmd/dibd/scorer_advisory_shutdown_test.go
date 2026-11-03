// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Drive run(), not a sender setter or shutdown helper. A private FIFO delays
// the real derived-memory write after ordinary mail commits. Shutdown must
// stay alive until the write is released, then exit successfully with all
// revisions written. This FIFO is a delay fixture, never reused as a memory
// file on another boot; the ordinary boot suite tests regular-file restarts.
func TestAdvisoryShutdownWaitsForDerivedMemory(t *testing.T) {
	repos := []string{advisoryBootRepo(t), advisoryBootRepo(t), advisoryBootRepo(t)}
	dir := privateDaemonFixtureDir(t)
	seedAdvisoryBoard(t, dir, repos[:2])
	pending := filepath.Join(dir, "scorer-advisories.json.tmp")
	if err := syscall.Mkfifo(pending, 0o600); err != nil {
		t.Fatal("setup delayed save:", err)
	}
	var saved []byte
	logs := advisoryDaemonBootWithShutdown(t, dir, repos, "0.25", "0", "1", func(exited chan error) {
		select {
		case err := <-exited:
			exited <- err // the fixture still owns and checks the actual exit
			t.Error("daemon exited before the committed advisory's delayed save was released")
		case <-time.After(250 * time.Millisecond):
		}
		fd, err := syscall.Open(pending, syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			t.Error("release delayed save:", err)
			return
		}
		defer func() { _ = syscall.Close(fd) }()
		// Raw nonblocking reads keep failure cleanup bounded even when the
		// unfixed daemon has already exited, leaving no writer to unblock.
		deadline := time.Now().Add(time.Second)
		var buffer [4096]byte
		for time.Now().Before(deadline) {
			n, err := syscall.Read(fd, buffer[:])
			if n > 0 {
				saved = append(saved, buffer[:n]...)
			}
			if err == nil && n == 0 && len(saved) > 0 {
				return
			}
			if err != nil && !errors.Is(err, syscall.EAGAIN) {
				t.Error("read delayed save:", err)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}, func(string) {
		if mail := committedScorerAdvice(t, dir); len(mail) != 1 {
			t.Fatalf("setup: advisory was not committed before interrupt: %d", len(mail))
		}
	})
	mail := committedScorerAdvice(t, dir)
	if len(mail) != 1 {
		t.Fatalf("setup: actual daemon committed %d advisories, want one\n%s", len(mail), logs)
	}
	var memory map[string]string
	if err := json.Unmarshal(saved, &memory); err != nil || len(memory) != len(repos) {
		t.Fatalf("shutdown did not finish the committed advisory save: %q (%v)\n%s", saved, err, logs)
	}
	for _, repo := range repos {
		if memory[scorerAdviceKey("advisory-fixture", repo)] == "" {
			t.Errorf("shutdown omitted repository revision %s", repo)
		}
	}
}
