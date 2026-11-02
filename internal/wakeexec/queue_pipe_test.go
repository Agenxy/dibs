// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package wakeexec

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQueueDrainFailureRequiresOneObservedExactItem(t *testing.T) {
	binary := staleQueueFixture(t)
	for _, mode := range []string{"valid", "missing", "wrong-timestamp", "dispatched", "observation-error", "ambiguous"} {
		t.Run(mode, func(t *testing.T) {
			home := staleQueueHome(t)
			t.Setenv("DIBS_TEST_QUEUE_PIPE_CASE", mode)
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = ln.Close() }()
			if err := ln.(*net.TCPListener).SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			t.Setenv("DIBS_TEST_QUEUE_PIPE_CONTROL", ln.Addr().String())
			var log bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&log, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			var observation QueueObservation
			done := make(chan bool, 1)
			argv := []string{binary, "queue", "--thread", "pipe-thread", "--message", Compose("notify")}
			fallback := []string{binary, "fallback-marker"}
			go func() {
				done <- RunCommandsObserved(argv, fallback, "worker", "", 5*time.Second, 100*time.Millisecond,
					func(value QueueObservation) { observation = value })
			}()
			controls := map[byte]net.Conn{}
			waited := false
			t.Cleanup(func() {
				for role, control := range controls {
					_ = control.SetDeadline(time.Now().Add(5 * time.Second))
					if role == 'g' {
						if _, err := control.Write([]byte{'x'}); err != nil {
							t.Errorf("release grandchild: %v", err)
						}
					} else if !waited {
						_, _ = control.Write([]byte{'e'})
					}
					if extra, err := io.ReadAll(control); err != nil || len(extra) != 0 {
						t.Errorf("%c exit receipt: %q %v", role, extra, err)
					}
					_ = control.Close()
				}
				if !waited {
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Error("wake fixture did not finish after release")
					}
				}
			})
			for range 2 {
				control, err := ln.Accept()
				if err != nil {
					t.Fatal("fixture control connection:", err)
				}
				_ = control.SetReadDeadline(time.Now().Add(5 * time.Second))
				var role [1]byte
				if _, err := io.ReadFull(control, role[:]); err != nil || (role[0] != 'p' && role[0] != 'g') || controls[role[0]] != nil {
					_ = control.Close()
					t.Fatalf("fixture role: %q %v", role, err)
				}
				controls[role[0]] = control
			}
			if _, err := controls['p'].Write([]byte{'e'}); err != nil {
				t.Fatal(err)
			}
			select {
			case accepted := <-done:
				waited = true
				if accepted != (mode == "valid") {
					t.Fatalf("accepted=%v for %s; observation=%+v log=%s", accepted, mode, observation, log.String())
				}
			case <-time.After(3 * time.Second):
				t.Fatal("retained stdout blocked the real wake route")
			}
			if mode == "valid" {
				if observation.Admission != "accepted" || observation.Pending != "pending" || observation.AgeSource != "notice" || observation.IssuedAt.IsZero() {
					t.Fatalf("acceptance did not record an observed app item: %+v", observation)
				}
			} else if observation.Admission != "unknown" || !strings.Contains(log.String(), "ErrWaitDelay") || !strings.Contains(log.String(), "observation=") {
				t.Fatalf("unknown lacked an honest drain/observation hint: %+v log=%s", observation, log.String())
			}
			if !readReceipt("pipe-thread").QueuedAt.IsZero() {
				t.Fatal("observed or unknown item was written as a local inference receipt")
			}
			probes, err := os.ReadFile(filepath.Join(home, "probe-processes"))
			if err != nil || strings.Count(string(probes), "probe\n") != 2 {
				t.Fatalf("want initial observation plus exactly one receipt observation: %q %v", probes, err)
			}
			if _, err := os.Stat(filepath.Join(home, "fallback-ran")); !os.IsNotExist(err) {
				t.Fatalf("ambiguous drain attempted fallback: %v", err)
			}
			if mode == "dispatched" {
				if data, err := os.ReadFile(filepath.Join(home, "dispatched.json")); err != nil || !bytes.Contains(data, []byte("pipe-thread")) {
					t.Fatalf("setup did not dispatch an actual committed item: %q %v", data, err)
				}
			}
			if mode == "ambiguous" {
				rows := staleQueueRows(t, home)
				if len(rows) != 2 || rows[0].ID == rows[1].ID {
					t.Fatalf("setup: want two distinct pending wakes: %+v", rows)
				}
				first, known := noticeIssuedAt(rows[0].Input[0].Text)
				second, otherKnown := noticeIssuedAt(rows[1].Input[0].Text)
				if !known || !otherKnown || !first.Equal(second) {
					t.Fatal("setup: the two wakes do not share the same whole-second timestamp")
				}
			}
		})
	}
}
