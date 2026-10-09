// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// sockPath is a unix socket path short enough to bind: macOS caps one at 104
// bytes, and t.TempDir under this test's name is past it.
func sockPath(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "sw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return filepath.Join(d, "s.sock")
}

// listenLines accepts on a session socket and streams every line it receives.
func listenLines(t *testing.T, sock string) <-chan string {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	lines := make(chan string, 16)
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					lines <- sc.Text()
				}
			}()
		}
	}()
	return lines
}

func collect(lines <-chan string, n int, within time.Duration) []string {
	var got []string
	deadline := time.After(within)
	for len(got) < n {
		select {
		case l := <-lines:
			got = append(got, l)
		case <-deadline:
			return got
		}
	}
	return got
}

// Two mail events two seconds apart must reach the session immediately,
// despite the historical 15-second cooldown, and leave no reminder timer.
func TestTwoMailEventsTwoSecondsApartDeliverWithoutATimer(t *testing.T) {
	sock := sockPath(t)
	lines := listenLines(t, sock)
	w := &selfWaker{socket: sock, token: "tok", cooldown: 15 * time.Second}
	if err := w.wake("first-mail"); err != nil {
		t.Fatal("setup:", err)
	}
	if got := collect(lines, 2, time.Second); len(got) != 2 {
		t.Fatalf("setup: first delivery %v", got)
	}
	<-time.After(2 * time.Second)
	if err := w.wake("second-mail"); err != nil {
		t.Fatal(err)
	}
	if got := collect(lines, 2, time.Second); len(got) != 2 {
		t.Fatalf("second mail was delayed: %v", got)
	}
	w.mu.Lock()
	pending, timer := w.pending, w.timer
	w.mu.Unlock()
	if pending || timer != nil {
		t.Fatal("successful events left a wake timer")
	}
	if extra := collect(lines, 1, 200*time.Millisecond); len(extra) != 0 {
		t.Fatal("extra delivery", extra)
	}
}

// A wake that delivered nothing spends no cooldown, or a busy socket at the
// first arrival silences the session for fifteen seconds.
func TestAFailedDeliverySpendsNoCooldown(t *testing.T) {
	sock := sockPath(t)
	w := &selfWaker{socket: sock, token: "tok", cooldown: time.Minute}
	if err := w.wake(testWakeNotice); err == nil {
		t.Fatal("setup: a wake with nobody listening reported success, so this proves nothing")
	}
	lines := listenLines(t, sock)
	if err := w.wake(testWakeNotice); err != nil {
		t.Fatal("the wake after the failed one errored:", err)
	}
	if got := collect(lines, 2, 2*time.Second); len(got) != 2 {
		t.Fatalf("after a failed attempt the next wake delivered %d line(s), want 2: the "+
			"failure spent the cooldown and the session stays silent for a minute", len(got))
	}
}
