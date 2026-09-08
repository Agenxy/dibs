package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// A refusal with an empty body produced no line at all, and a body cut
// short mid-JSON was forwarded as malformed JSON: neither is a response the
// harness can match to its request, and both leave the call hanging.
func TestAnEmptyOrTruncatedReplyIsStillAnswered(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"an empty 503", http.StatusServiceUnavailable, "", "503"},
		{"an empty 502", http.StatusBadGateway, "", "502"},
		{"a body cut short mid-JSON", http.StatusOK, `{"jsonrpc":"2.0","id":7,"result":{"content":[{"te`, "cut short"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			var buf bytes.Buffer
			out := &syncWriter{w: bufio.NewWriter(&buf)}
			line := []byte(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"check_in"}}`)
			req, err := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(line))
			if err != nil {
				t.Fatal(err)
			}
			forward(srv.Client(), req, line, out, nil)
			got := strings.TrimSpace(buf.String())
			if got == "" {
				t.Fatal("the harness got no line at all: the call hangs")
			}
			var reply struct {
				ID    json.RawMessage `json:"id"`
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(got), &reply); err != nil {
				t.Fatalf("the harness read %q, which is not JSON-RPC: %v", got, err)
			}
			if string(reply.ID) != "7" || !strings.Contains(reply.Error.Message, tc.want) {
				t.Fatalf("the reply is %q: want id 7 and an error naming %q", got, tc.want)
			}
		})
	}
}

// The in-place upgrade delivered the notice the old image owed through a
// waker of its own, beside the one the restored streams write through, so a
// notification arriving during the restore put two interruptions into the
// session at once. The owed notice goes through the watcher's waker and
// shares its cooldown.
func TestARestoredPendingWakeSharesTheSessionCooldown(t *testing.T) {
	sock := sockPath(t)
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", sock)
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "child-token")
	resetWakeStreams()
	t.Cleanup(resetWakeStreams)
	t.Cleanup(func() { recordWakePending(false) })
	lines := listenLines(t, sock)
	hold := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, `data: {"jsonrpc":"2.0","method":"notifications/resources/updated","params":{"uri":"dibs://inbox","_meta":{"com.dibs/event":"message.sent","com.dibs/msg_type":"question","com.dibs/serial":9}}}`+"\n\n")
		fl.Flush()
		<-hold
	}))
	defer srv.Close()
	defer close(hold)
	blob, err := json.Marshal(bridgeState{
		WakeStreams: []wakeHandoff{{Key: "busy", Token: "carried-token", Since: 4}}, WakePending: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(bridgeStateEnv, string(blob))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	iw := inboxWatcher{cooldown: 700 * time.Millisecond}
	var streams sync.WaitGroup
	out := &syncWriter{w: bufio.NewWriter(io.Discard)}
	restoreCarried(ctx, srv.Client(), srv.URL, "secret", out, &streams, &iw, true)
	// One notice at once: the owed one and the arriving one share a cooldown.
	if got := collect(lines, 4, 400*time.Millisecond); len(got) != 2 {
		t.Fatalf("the restore put %d line(s) into the session at once, want 2: the owed notice and "+
			"the arriving one went through two wakers", len(got))
	}
	// And the second is not dropped: it follows when the cooldown ends.
	if got := collect(lines, 2, 3*time.Second); len(got) != 2 {
		t.Fatalf("after the cooldown %d line(s) arrived, want 2", len(got))
	}
}

// The listen request names the session the stream serves, so the daemon can
// withhold the inbox while the agent is in another one.
func TestTheListenRequestNamesTheSessionItServes(t *testing.T) {
	var iw inboxWatcher
	var req struct {
		Params struct {
			Meta map[string]any `json:"_meta"`
		} `json:"params"`
	}
	// Built the way startFor builds one: the stream carries the session it was
	// created to serve, and re-states that on every reconnect.
	st := &inboxStream{token: "tok", session: streamSession()}
	if err := json.Unmarshal(iw.listenBody(st), &req); err != nil {
		t.Fatal(err)
	}
	if want := sessionID(); want == "" || req.Params.Meta["com.dibs/session"] != want {
		t.Fatalf("the listen request carries session %v, want this bridge's own %q", req.Params.Meta["com.dibs/session"], want)
	}
}

// The self-wake stream says it serves the thread the harness named on its
// tool calls, in preference to the process-derived session id, and an
// in-place upgrade carries that name across.
func TestTheListenRequestNamesTheThreadTheHarnessNamed(t *testing.T) {
	t.Cleanup(func() { noteThread("") })
	noteThread("")
	line := []byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"check_in",` +
		`"arguments":{},"_meta":{"threadId":"019a1b2c-3d4e-7f80-9a1b-2c3d4e5f6a7b"}}}`)
	enrichRegister(line)
	var iw inboxWatcher
	var req struct {
		Params struct {
			Meta map[string]any `json:"_meta"`
		} `json:"params"`
	}
	if err := json.Unmarshal(iw.listenBody(&inboxStream{token: "tok", session: streamSession()}), &req); err != nil {
		t.Fatal(err)
	}
	if got := req.Params.Meta["com.dibs/session"]; got != "019a1b2c-3d4e-7f80-9a1b-2c3d4e5f6a7b" {
		t.Fatalf("the listen request says it serves %v, want the thread the harness named", got)
	}
	// Across an upgrade.
	st := handoffState()
	if st.Thread != "019a1b2c-3d4e-7f80-9a1b-2c3d4e5f6a7b" {
		t.Fatalf("the handoff carries thread %q", st.Thread)
	}
	noteThread("")
	blob, _ := json.Marshal(st)
	t.Setenv(bridgeStateEnv, string(blob))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var streams sync.WaitGroup
	out := &syncWriter{w: bufio.NewWriter(io.Discard)}
	restoreCarried(ctx, &http.Client{}, "http://127.0.0.1:1/mcp", "secret", out, &streams, &iw, false)
	if threadServed() != "019a1b2c-3d4e-7f80-9a1b-2c3d4e5f6a7b" {
		t.Fatalf("after the upgrade the bridge serves thread %q: the restored streams say the wrong session", threadServed())
	}
}
