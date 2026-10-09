// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Enter through the subscription, not a manually installed retry callback.
// Replace only the ambiguous kernel failure; the retry must carry the failed
// offer ID through the real watcher wiring and write the daemon's fresh body.
func TestBridgeSubscriptionRetriesTheOriginalFailedOfferOnce(t *testing.T) {
	sock := sockPath(t)
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", sock)
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "child-token")
	resetWakeStreams()
	t.Cleanup(resetWakeStreams)
	t.Cleanup(func() { recordWakePending(false, "") })
	lines := listenLines(t, sock)
	var retries atomic.Int32
	hold := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params struct {
				Meta map[string]any `json:"_meta"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Method == "subscriptions/listen" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, `data: {"method":"notifications/resources/updated","params":{"_meta":{"com.dibs/event":"message.sent","com.dibs/msg_type":"question","com.dibs/serial":7,"com.dibs/digest":"captured","com.dibs/socket_offer":true,"com.dibs/socket_batch":true}}}`+"\n\n")
			w.(http.Flusher).Flush()
			<-hold
			return
		}
		meta := req.Params.Meta
		text, id := "original pending mail", "first-offer"
		outMeta := map[string]any{}
		if meta["com.dibs/socket_offer_id"] != nil {
			text, id = "", ""
		} else if retry, _ := meta["com.dibs/socket_retry_offer"].(string); retry != "" {
			if retry != "first-offer" {
				t.Errorf("retry changed original offer: %q", retry)
			}
			retries.Add(1)
			text, id = "fresh original pending mail", "retry-offer"
			outMeta["com.dibs/socket_retry_offer"] = retry
		}
		outMeta["com.dibs/socket_offer_id"] = id
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{
			"_meta": outMeta, "contents": []any{map[string]any{"text": text}},
		}})
	}))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); close(hold); srv.Close() })
	iw := &inboxWatcher{cooldown: 50 * time.Millisecond}
	waker := iw.sharedWaker()
	if waker == nil {
		t.Fatal("setup: no own-session writer")
	}
	var attempts atomic.Int32
	waker.deliverFn = func(notice string) error {
		if attempts.Add(1) == 1 {
			return syscall.ETIMEDOUT
		}
		return waker.deliver(notice)
	}
	iw.startFor(ctx, srv.Client(), srv.URL, "secret", "worker", "token", 0)
	got := collect(lines, 2, 3*time.Second)
	if len(got) != 2 || !strings.Contains(got[1], "fresh original pending mail") {
		t.Fatalf("retry did not deliver the freshly fenced original offer: %v", got)
	}
	if attempts.Load() != 2 || retries.Load() != 1 {
		t.Fatalf("attempts=%d retries=%d", attempts.Load(), retries.Load())
	}
	if got := collect(lines, 1, 200*time.Millisecond); len(got) != 0 {
		t.Fatal("third automatic write", got)
	}
	if wakeIsPending() {
		t.Fatal("settled retry remains in the upgrade handoff")
	}
}
