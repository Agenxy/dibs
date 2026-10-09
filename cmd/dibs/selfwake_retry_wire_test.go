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

// A dropped SSE stream replays the same original serial before its retry.
// Reconnect is transport recovery, not another mail event or attempt budget.
func TestBridgeReconnectDoesNotRearmTheOriginalFailedDelivery(t *testing.T) {
	t.Run("retry succeeds", func(t *testing.T) { testBridgeReconnectAttemptBudget(t, false) })
	t.Run("retry fails", func(t *testing.T) { testBridgeReconnectAttemptBudget(t, true) })
}

func testBridgeReconnectAttemptBudget(t *testing.T, failRetry bool) {
	sock := sockPath(t)
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", sock)
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "child-token")
	resetWakeStreams()
	t.Cleanup(resetWakeStreams)
	t.Cleanup(func() { recordWakePending(false, "") })
	lines := listenLines(t, sock)
	var streams, attempts, retries atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string
			Params struct {
				Meta map[string]any `json:"_meta"`
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Method == "subscriptions/listen" {
			streams.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, `data: {"method":"notifications/resources/updated","params":{"_meta":{"com.dibs/event":"message.sent","com.dibs/msg_type":"question","com.dibs/serial":7,"com.dibs/digest":"captured","com.dibs/socket_offer":true,"com.dibs/socket_batch":true}}}`+"\n\n")
			return // actual HTTP EOF forces reconnect before the retry fires
		}
		meta := req.Params.Meta
		text, id := "original pending mail", "first-offer"
		outMeta := map[string]any{}
		if meta["com.dibs/socket_offer_id"] != nil {
			text, id = "", ""
		} else if retry, _ := meta["com.dibs/socket_retry_offer"].(string); retry != "" {
			if retry != "first-offer" {
				t.Errorf("retry changed cause: %q", retry)
			}
			retries.Add(1)
			text, id = "fresh original pending mail", "retry-offer"
			outMeta["com.dibs/socket_retry_offer"] = retry
		}
		outMeta["com.dibs/socket_offer_id"] = id
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"_meta": outMeta, "contents": []any{map[string]any{"text": text}}}})
	}))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); srv.Close() })
	iw := &inboxWatcher{cooldown: 200 * time.Millisecond, reconnect: 20 * time.Millisecond}
	waker := iw.sharedWaker()
	if waker == nil {
		t.Fatal("setup: no own-session writer")
	}
	t.Cleanup(func() {
		waker.mu.Lock()
		if waker.timer != nil {
			waker.timer.Stop()
		}
		waker.mu.Unlock()
	})
	waker.deliverFn = func(notice string) error {
		if n := attempts.Add(1); n == 1 || failRetry {
			return syscall.ETIMEDOUT
		}
		return waker.deliver(notice)
	}
	iw.startFor(ctx, srv.Client(), srv.URL, "secret", "worker", "token", 0)
	if failRetry {
		got := collect(lines, 1, 550*time.Millisecond)
		if streams.Load() < 2 {
			t.Fatal("setup: stream did not reconnect")
		}
		if len(got) != 0 || attempts.Load() != 2 || retries.Load() != 1 || waker.canReach() {
			t.Fatalf("failed replay reset the cap: streams=%d attempts=%d retries=%d reachable=%v", streams.Load(), attempts.Load(), retries.Load(), waker.canReach())
		}
		return
	}
	got := collect(lines, 2, 3*time.Second)
	if len(got) != 2 {
		t.Fatal("original retry delivered nothing", got)
	}
	// Keep reconnecting after success too: the serial must stay fenced even
	// though successful delivery and cursor advancement happen on the timer.
	extra := collect(lines, 1, 350*time.Millisecond)
	if streams.Load() < 2 {
		t.Fatal("setup: stream did not reconnect")
	}
	if len(extra) != 0 || attempts.Load() != 2 || retries.Load() != 1 || !strings.Contains(got[1], "fresh original pending mail") {
		t.Fatalf("replay reset the attempt budget: streams=%d attempts=%d retries=%d first=%v extra=%v", streams.Load(), attempts.Load(), retries.Load(), got, extra)
	}
}
