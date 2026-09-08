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

// A second arrival inside the cooldown is coalesced, not dropped: one notice
// follows when the cooldown ends. Returning success and forgetting it left an
// agent that had already read its inbox unaware of the second message.
func TestAnArrivalInTheCooldownIsDeliveredWhenItEnds(t *testing.T) {
	sock := sockPath(t)
	lines := listenLines(t, sock)
	w := &selfWaker{socket: sock, token: "tok", cooldown: 300 * time.Millisecond}
	started := time.Now()
	if err := w.wake(selfWakeNotice); err != nil {
		t.Fatal("setup: the first notice failed:", err)
	}
	if err := w.wake(selfWakeNotice); err != nil {
		t.Fatal("the second wake, inside the cooldown, errored instead of coalescing:", err)
	}
	got := collect(lines, 4, 3*time.Second)
	if len(got) != 4 {
		t.Fatalf("the session socket received %d line(s), want 4 (auth+notice, twice): the "+
			"second arrival was dropped by the cooldown and nothing followed it", len(got))
	}
	if since := time.Since(started); since < 250*time.Millisecond {
		t.Errorf("the deferred notice arrived %v after the first: it did not wait out the cooldown", since)
	}
	if extra := collect(lines, 1, 500*time.Millisecond); len(extra) != 0 {
		t.Errorf("a third notice arrived for two arrivals: the deferred one did not coalesce")
	}
}

// A wake that delivered nothing spends no cooldown, or a busy socket at the
// first arrival silences the session for fifteen seconds.
func TestAFailedDeliverySpendsNoCooldown(t *testing.T) {
	sock := sockPath(t)
	w := &selfWaker{socket: sock, token: "tok", cooldown: time.Minute}
	if err := w.wake(selfWakeNotice); err == nil {
		t.Fatal("setup: a wake with nobody listening reported success, so this proves nothing")
	}
	lines := listenLines(t, sock)
	if err := w.wake(selfWakeNotice); err != nil {
		t.Fatal("the wake after the failed one errored:", err)
	}
	if got := collect(lines, 2, 2*time.Second); len(got) != 2 {
		t.Fatalf("after a failed attempt the next wake delivered %d line(s), want 2: the "+
			"failure spent the cooldown and the session stays silent for a minute", len(got))
	}
}
