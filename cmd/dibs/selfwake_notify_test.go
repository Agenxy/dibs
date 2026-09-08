package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// wakesFor runs one watcher against a stream that delivers exactly the given
// notification, and reports whether a notice reached the session socket.
func wakesFor(t *testing.T, notification string) bool {
	t.Helper()
	dir := t.TempDir()
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	lines := make(chan string, 8)
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
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", sock)
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "child-token")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "data: "+notification+"\n\n")
		fl.Flush()
		time.Sleep(1500 * time.Millisecond)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var w inboxWatcher
	w.start(ctx, srv.Client(), srv.URL, "local-secret", "agent-token")
	select {
	case <-lines:
		return true
	case <-time.After(1200 * time.Millisecond):
		return false
	}
}

// A notify is news nobody is blocked on: the daemon's waker never starts a
// process for one, and the bridge must not put a notice into the session for
// one either. A question still wakes, and so does a notification from a daemon
// too old to say what arrived.
func TestANotifyDoesNotWakeTheSession(t *testing.T) {
	const updated = `{"jsonrpc":"2.0","method":"notifications/resources/updated","params":{"uri":"dibs://inbox"`
	if wakesFor(t, updated+`,"_meta":{"com.dibs/event":"message.sent","com.dibs/msg_type":"notify"}}}`) {
		t.Error("a notify put a notice into the session: the bridge interrupts a turn for " +
			"mail the daemon's own waker would leave for the next check_in")
	}
	if !wakesFor(t, updated+`,"_meta":{"com.dibs/event":"message.sent","com.dibs/msg_type":"question"}}}`) {
		t.Error("a question did not wake the session, so the rule above is a mute, not a filter")
	}
	if !wakesFor(t, updated+`}}`) {
		t.Error("a notification that names no event did not wake: a bridge newer than its " +
			"daemon goes silent instead of erring towards the notice")
	}
}
