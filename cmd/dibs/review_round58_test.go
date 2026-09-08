package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The cooldown is a promise about the session's socket, not about a
// mailbox: each stream had a waker of its own, so two mailboxes receiving
// questions produced two immediate interruptions microseconds apart. Every
// stream writes through the one waker, and a second arrival inside the
// cooldown is deferred to its end.
func TestTwoMailboxesShareOneSessionCooldown(t *testing.T) {
	sock := sockPath(t)
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", sock)
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "child-token")
	resetWakeStreams()
	t.Cleanup(resetWakeStreams)
	t.Cleanup(func() { recordWakePending(false) })
	lines := listenLines(t, sock)
	hold := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Params struct {
				Meta map[string]any `json:"_meta"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		tok, _ := req.Params.Meta["com.dibs/token"].(string)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		serial := 7
		if tok == "tok-second" {
			serial = 8
		}
		_, _ = fmt.Fprintf(w, `data: {"jsonrpc":"2.0","method":"notifications/resources/updated","params":{"uri":"dibs://inbox","_meta":{"com.dibs/event":"message.sent","com.dibs/msg_type":"question","com.dibs/serial":%d}}}`+"\n\n", serial)
		fl.Flush()
		<-hold
	}))
	defer srv.Close()
	defer close(hold)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	iw := inboxWatcher{cooldown: 700 * time.Millisecond}
	iw.startFor(ctx, srv.Client(), srv.URL, "secret", "first", "tok-first", 0)
	iw.startFor(ctx, srv.Client(), srv.URL, "secret", "second", "tok-second", 0)
	// One notice at once: two lines, the auth line and the notice.
	got := collect(lines, 4, 400*time.Millisecond)
	if len(got) != 2 {
		t.Fatalf("two mailboxes with a question each put %d line(s) into the session at once, want 2: "+
			"two interruptions inside one cooldown", len(got))
	}
	// The second is not dropped: it follows when the cooldown ends.
	if got := collect(lines, 2, 3*time.Second); len(got) != 2 {
		t.Fatalf("the second mailbox's notice never followed the cooldown: %d line(s)", len(got))
	}
}
