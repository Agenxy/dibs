// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package wakeexec

import (
	"bytes"
	"fmt"
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
	for _, mode := range []string{"valid", "missing", "wrong-timestamp", "dispatched", "observation-error", "ambiguous", "nonqueue"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "missing" {
				t.Setenv("DIBS_TEST_QUEUE_COLD_RECEIPT", filepath.Join(t.TempDir(), "cold-receipt"))
			}
			retryUnreadyQueueFixture(t, func() error { return runQueueDrainFixture(t, binary, mode) })
		})
	}
}

func runQueueDrainFixture(t *testing.T, binary, mode string) error {
	t.Helper()
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
	defer slog.SetDefault(previous)
	var observation QueueObservation
	done := make(chan bool, 1)
	argv := []string{binary, "queue", "--thread", "pipe-thread", "--message", Compose("notify")}
	if mode == "nonqueue" {
		argv = []string{binary, "nonqueue-pipe"}
	}
	fallback := []string{binary, "fallback-marker"}
	go func() {
		done <- RunCommandsObserved(argv, fallback, "worker", "", 5*time.Second, 100*time.Millisecond,
			func(value QueueObservation) { observation = value }).OK
	}()
	controls := map[byte]net.Conn{}
	waited := false
	defer func() {
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
	}()
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
	var accepted bool
	select {
	case accepted = <-done:
		waited = true
	case <-time.After(3 * time.Second):
		t.Fatal("retained stdout blocked the real wake route")
	}
	if !readReceipt("pipe-thread").QueuedAt.IsZero() {
		t.Fatal("observed or unknown item was written as a local inference receipt")
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
	if err := checkPipeProbeReadiness(t, home, mode); err != nil {
		return err
	}
	if accepted != (mode == "valid") {
		t.Fatalf("accepted=%v for %s; observation=%+v log=%s", accepted, mode, observation, log.String())
	}
	switch mode {
	case "valid":
		if observation.Admission != "accepted" || observation.Pending != "pending" || observation.AgeSource != "notice" || observation.IssuedAt.IsZero() {
			t.Fatalf("acceptance did not record an observed app item: %+v", observation)
		}
	case "nonqueue":
		if observation != (QueueObservation{}) || !strings.Contains(log.String(), "ErrWaitDelay") || !strings.Contains(log.String(), "no app receipt contract") {
			t.Fatalf("non-queue drain failure lacked its warning: %+v log=%s", observation, log.String())
		}
	default:
		reason := map[string]string{
			"missing": "missing_or_dispatched", "dispatched": "missing_or_dispatched",
			"wrong-timestamp": "wrong_timestamp", "observation-error": "observation_error", "ambiguous": "ambiguous",
		}[mode]
		if observation.Admission != "unknown" || !strings.Contains(log.String(), "ErrWaitDelay") ||
			!strings.Contains(log.String(), "observation="+reason) {
			t.Fatalf("unknown lacked an honest drain/observation hint: %+v log=%s", observation, log.String())
		}
	}
	return nil
}

func checkPipeProbeReadiness(t *testing.T, home, mode string) error {
	t.Helper()
	var missing error
	for _, file := range []string{"probe-processes", "probe-readiness"} {
		probes, err := os.ReadFile(filepath.Join(home, file))
		if mode == "nonqueue" {
			if !os.IsNotExist(err) {
				t.Fatalf("non-queue route recorded %s: %q %v", file, probes, err)
			}
			continue
		}
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("read app observation %s: %v", file, err)
		}
		// Probes have exited. A missing signal requires a fresh fixture,
		// while duplicate or out-of-order signals fail the exact count.
		switch string(probes) {
		case "admission\nreceipt\n":
		case "", "admission\n", "receipt\n":
			missing = fmt.Errorf("%s needs one admission and one receipt, got %q", file, probes)
		default:
			t.Fatalf("%s must record exactly one admission and one receipt: %q", file, probes)
		}
	}
	return missing
}
