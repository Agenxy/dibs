package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The bridge puts a notice into its own session when its agent gets mail.
//
// This is the route that actually reaches an idle Claude Code session. The
// daemon's peer-socket wake connects from outside and is HELD by any receiver
// in bypassPermissions mode, with no receipt, so it fails invisibly. A message
// from the session's own descendants is accepted instead, and the bridge is
// one: the harness spawns it as a direct child and hands it
// CLAUDE_CODE_MESSAGING_SOCKET and CLAUDE_CODE_MESSAGING_TOKEN.
//
// Both halves are faked here, deliberately: a real daemon and a real harness
// would make this an integration test that cannot run in CI, and the thing
// worth pinning is that an inbox notification becomes exactly one authenticated
// line on the session socket.
// testWakeNotice is any notice at all, for the tests about the waker's
// MECHANICS: the cooldown, the deferral, the retry and the upgrade handoff.
//
// A test constant rather than the product's own string, because the product no
// longer has one. What the in-session route says is whatever the daemon put on
// the notification (mcp.DigestMetaKey), so a payload the waker is asked to
// deliver is now a parameter and nothing in these tests depends on its
// contents. The rules they assert are about counting and timing, and they held
// against a fixed sentence for exactly the same reason they hold against this.
const testWakeNotice = "a notice, for a test about when notices are sent"

// wakeIsPending is currentWakePending's bool alone: these tests ask whether a
// notice is owed, and the handoff now carries what it says as well.
// fixtureDigest is what the fake daemon puts on its notification, quotes and
// all: the bridge must deliver it byte for byte.
const fixtureDigest = `mail for your agent "worker".`

func wakeIsPending() bool {
	owed, _ := currentWakePending()
	return owed
}

func TestTheBridgeWakesItsOwnSession(t *testing.T) {
	// A stand-in for the harness's session socket.
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
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "child-token-abc")

	// A stand-in for the daemon: acknowledge, then push one inbox update.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/subscriptions/acknowledged\"}\n\n")
		fl.Flush()
		_, _ = fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/resources/updated\",\"params\":{\"uri\":\"dibs://inbox\",\"_meta\":{\"com.dibs/digest\":\"mail for your agent \\\"worker\\\".\"}}}\n\n")
		fl.Flush()
		time.Sleep(300 * time.Millisecond)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	var w inboxWatcher
	w.start(ctx, srv.Client(), srv.URL, "local-secret", "agent-token-xyz")

	var got []string
	deadline := time.After(6 * time.Second)
	for len(got) < 2 {
		select {
		case l := <-lines:
			got = append(got, l)
		case <-deadline:
			t.Fatalf("the session socket received %d line(s), want 2 (auth + notice): %v.\n"+
				"  An inbox notification that produces nothing is the failure this "+
				"whole path exists to remove.", len(got), got)
		}
	}

	var auth struct {
		Type, Token string
	}
	if err := json.Unmarshal([]byte(got[0]), &auth); err != nil {
		t.Fatalf("first line is not JSON: %q", got[0])
	}
	if auth.Type != "auth" || auth.Token != "child-token-abc" {
		t.Errorf("first line must authenticate with the harness's own child token, got %q", got[0])
	}
	var msg struct {
		Type    string `json:"type"`
		Message struct {
			Role, Content string
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(got[1]), &msg); err != nil {
		t.Fatalf("second line is not JSON: %q", got[1])
	}
	// EXACTLY WHAT THE DAEMON SENT, which is the whole point of the route now.
	// The bridge composed its own sentence here for three releases and the
	// daemon's digest arrived separately, on the same socket, a moment later:
	// two cards per message, the emptier one on top. This end adds nothing and
	// subtracts nothing.
	if msg.Type != "user" || msg.Message.Content != fixtureDigest {
		t.Errorf("second line = %q; want a user message carrying exactly the digest the "+
			"daemon sent, %q", got[1], fixtureDigest)
	}
}

// A harness that publishes no socket gets no waker, and that is not an error.
func TestNoSessionSocketIsNotAFailure(t *testing.T) {
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", "")
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "")
	if w := newSelfWaker(); w != nil {
		t.Error("a waker was built with no socket to write to")
	}
	if err := (*selfWaker)(nil).wake("x"); err == nil {
		t.Error("waking through no route must report that it could not, not claim success")
	}
	_ = os.Getenv
}
