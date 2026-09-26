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
func TestMailWakesTheSession(t *testing.T) {
	const updated = `{"jsonrpc":"2.0","method":"notifications/resources/updated","params":{"uri":"dibs://inbox"`
	// The digest is what the bridge SENDS, so every case that should wake has
	// to carry one: this route no longer composes a line of its own. See
	// selfWakeLine and mcp.DigestMetaKey.
	const digest = `,"com.dibs/digest":"mail for your agent \"worker\"."`
	// A notify too, and that is the correction.
	//
	// This asserted the opposite, matching a daemon-side rule that said only
	// blocking mail was worth a wake. That rule was wrong in the one place it
	// mattered: it also governed the route that reaches an agent whose session
	// has STOPPED, so an FYI reached nobody until its operator happened to
	// mention it. Mail wakes an agent; how much mail is `[wake] policy`, and
	// the daemon applies it before it notifies anybody, so this bridge never
	// hears about mail the operator asked not to be interrupted for.
	if !wakesFor(t, updated+`,"_meta":{"com.dibs/event":"message.sent","com.dibs/msg_type":"notify"`+digest+`}}}`) {
		t.Error("a notify did not put a notice into the session: mail wakes an agent, " +
			"and this bridge is only told about mail the board already decided to wake for")
	}
	if !wakesFor(t, updated+`,"_meta":{"com.dibs/event":"message.sent","com.dibs/msg_type":"question"`+digest+`}}}`) {
		t.Error("a question did not wake the session either")
	}
	// Still a filter, not a pass-through: mail LEAVING is not mail arriving.
	if wakesFor(t, updated+`,"_meta":{"com.dibs/event":"message.acked","com.dibs/msg_type":"question"`+digest+`}}}`) {
		t.Error("an ack woke the session: that is this agent's own mail being closed, " +
			"and waking for it is how a wake becomes an echo")
	}
	// A NOTIFICATION WITH NO DIGEST WAKES NOBODY, AND THAT IS THE CHANGE.
	//
	// It used to, on the reasoning that a bridge newer than its daemon should
	// err towards the notice rather than go silent. That reasoning was sound
	// about the wake and wrong about the socket, because it had only ever
	// counted one writer. A daemon too old to send a digest is also too old to
	// stand down, so it is writing its own notice to this same session socket:
	// a line from here is not a wake that would otherwise be missed, it is the
	// SECOND card, and it is the one with no information in it. That pair is
	// what the operator was looking at, four times, across three releases that
	// each reworded the wrong half of it.
	//
	// Nothing is lost. The old daemon delivers, and once it is upgraded it
	// sends the digest and this route carries it.
	if wakesFor(t, updated+`}}`) {
		t.Error("a notification carrying no digest still put a line into the session: " +
			"the daemon that sent it has not stood down, so this is a second " +
			"notification for one message and it is the empty one")
	}
	if wakesFor(t, updated+`,"_meta":{"com.dibs/event":"message.sent","com.dibs/msg_type":"question"}}}`) {
		t.Error("a notification naming the message type but carrying no digest woke the " +
			"session: composing a line from the type is what the last release did, and " +
			"it is still a second card in front of the daemon's own")
	}
}
