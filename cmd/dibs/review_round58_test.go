// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

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

// Both mailboxes use the same socket writer, without a successful-write timer.
func TestTwoMailboxesDeliverImmediatelyThroughOneSessionWriter(t *testing.T) {
	sock := sockPath(t)
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", sock)
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "child-token")
	resetWakeStreams()
	t.Cleanup(resetWakeStreams)
	t.Cleanup(func() { recordWakePending(false, "") })
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
		_, _ = fmt.Fprintf(w, `data: {"jsonrpc":"2.0","method":"notifications/resources/updated","params":{"uri":"dibs://inbox","_meta":{"com.dibs/event":"message.sent","com.dibs/msg_type":"question","com.dibs/serial":%d,"com.dibs/digest":"mail for your agent."}}}`+"\n\n", serial)
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
	got := collect(lines, 4, 400*time.Millisecond)
	if len(got) != 4 {
		t.Fatalf("two mailboxes did not deliver both notices immediately: %d line(s)", len(got))
	}
	if got := collect(lines, 1, 2*iw.cooldown); len(got) != 0 {
		t.Fatalf("successful notices repeated on a timer: %d line(s)", len(got))
	}
}
