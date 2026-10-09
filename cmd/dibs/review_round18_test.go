// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// A wake that failed keeps its notification: the cursor does not pass it, so
// a reconnect replays it.
func TestAFailedWakeKeepsItsNotification(t *testing.T) {
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", "/nonexistent/but/present.sock")
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "child-token")
	bodies := make(chan map[string]any, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(raw, &req)
		bodies <- req
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, `data: {"jsonrpc":"2.0","method":"notifications/subscriptions/acknowledged","params":{"notifications":{},"_meta":{"com.dibs/serial":5}}}`+"\n\n")
		_, _ = fmt.Fprint(w, `data: {"jsonrpc":"2.0","method":"notifications/resources/updated","params":{"uri":"dibs://inbox","_meta":{"com.dibs/event":"message.sent","com.dibs/msg_type":"question","com.dibs/serial":7,"com.dibs/digest":"mail for your agent."}}}`+"\n\n")
		fl.Flush()
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w := inboxWatcher{reconnect: 50 * time.Millisecond}
	w.start(ctx, srv.Client(), srv.URL, "local-secret", "agent-token")
	<-bodies
	select {
	case second := <-bodies:
		params, _ := second["params"].(map[string]any)
		meta, _ := params["_meta"].(map[string]any)
		if got, _ := meta["com.dibs/since"].(float64); got != 5 {
			t.Fatalf("after a wake that could not be delivered the reconnect carries since=%v, want 5: "+
				"the question's notification is consumed and nothing replays it", meta["com.dibs/since"])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the watcher did not reconnect")
	}
}

// An ambiguous failed write retries once without another arrival. A gone
// socket instead surrenders immediately, covered by the recovered-socket guard.
func TestAFailedWakeIsRetriedWhenTheSocketReturns(t *testing.T) {
	sock := sockPath(t)
	lines := listenLines(t, sock)
	w := &selfWaker{socket: sock, token: "tok", cooldown: 200 * time.Millisecond}
	var attempts atomic.Int32
	w.deliverFn = func(notice string) error {
		if attempts.Add(1) == 1 {
			return syscall.ETIMEDOUT
		}
		return w.deliver(notice)
	}
	if err := w.wake(testWakeNotice); err == nil {
		t.Fatal("setup: an ambiguous failed write reported success")
	}
	if got := collect(lines, 2, 2*time.Second); len(got) != 2 {
		t.Fatalf("after the socket came back %d line(s) arrived without another wake, want 2: "+
			"the agent stays asleep on stored mail until something else arrives", len(got))
	}
}
