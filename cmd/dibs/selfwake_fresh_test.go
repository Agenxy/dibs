// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
	"github.com/agenxy/dibs/internal/mcp"
)

// Drive the real listen -> arrival batch -> session socket path. Acknowledging
// after observation but before the write must make its fresh digest empty.
func TestBusySelfWakeNeverSchedulesAcknowledgedMail(t *testing.T) {
	sock := sockPath(t)
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", sock)
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "child-token")
	resetWakeStreams()
	t.Cleanup(resetWakeStreams)
	t.Cleanup(func() { recordWakePending(false, "") })
	lines := listenLines(t, sock)
	const fixtureSession = "81f97290-0001-4000-8000-111111111111"
	previousThread := threadServed()
	noteThread(fixtureSession)
	t.Cleanup(func() { noteThread(previousThread) })
	st := core.NewState("fresh", core.DefaultLimits())
	for _, id := range []string{"sender", "worker"} {
		sid := ""
		if id == "worker" {
			sid = fixtureSession
		}
		if _, _, err := st.Apply(&core.Op{
			Kind: core.OpRegister, Name: id, NewToken: "tok-" + id,
			SessionID: sid,
		}, time.Now()); err != nil {
			t.Fatal("register setup:", err)
		}
	}
	dir := t.TempDir()
	key, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "fresh", key)
	if err != nil {
		t.Fatal(err)
	}
	engineCtx, stopEngine := context.WithCancel(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	eng := engine.New(st, led, nil)
	go eng.Run(engineCtx)
	srv := httptest.NewServer(mcp.New(eng))
	t.Cleanup(func() { cancel(); srv.Close(); stopEngine(); _ = led.Close() })
	iw := &inboxWatcher{cooldown: 500 * time.Millisecond}
	iw.startFor(ctx, srv.Client(), srv.URL, "test-secret", "worker", "tok-worker", 0)
	for deadline := time.Now().Add(time.Second); ; {
		live, _ := eng.SelfWaking("worker")
		if live {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("setup: self-wake subscription never claimed the socket")
		}
		time.Sleep(time.Millisecond)
	}
	send := func(body string) uint64 {
		t.Helper()
		res, err := eng.Do(ctx, &core.Op{
			Kind: core.OpSendMessage, Token: "tok-sender", To: "worker",
			MsgType: core.MsgHandoff, Body: body,
		})
		if err != nil {
			t.Fatal("send setup:", err)
		}
		return res["msg_serial"].(uint64)
	}
	ack := func(serial uint64) {
		t.Helper()
		if _, err := eng.Do(ctx, &core.Op{Kind: core.OpAckMessage, Token: "tok-worker", MsgSerial: serial}); err != nil {
			t.Fatal("ack setup:", err)
		}
	}
	if _, err := eng.HookPoll(ctx, streamSession(), "Stop", "", true, false); err != nil {
		t.Fatal("setup: establish the idle lifecycle:", err)
	}
	first := send("first notice")
	if got := collect(lines, 2, time.Second); len(got) != 2 {
		t.Fatalf("setup: initial notice did not reach the session: %v", got)
	}
	ack(first)
	second := send("stale-digest-marker")
	for deadline := time.Now().Add(150 * time.Millisecond); !watcherAttempted(iw, second); {
		if time.Now().After(deadline) {
			t.Fatal("setup: second notification was never observed")
		}
		time.Sleep(time.Millisecond)
	}
	ack(second)
	if got := collect(lines, 2, time.Second); len(got) != 0 {
		t.Fatalf("a scheduled wake sent a digest for acknowledged mail: %v", got)
	}
}

func watcherAttempted(iw *inboxWatcher, serial uint64) bool {
	iw.mu.Lock()
	defer iw.mu.Unlock()
	for _, stream := range iw.streams {
		if stream.attempted >= serial {
			return true
		}
	}
	return false
}

func TestRestoredMixedCapabilitiesRefreshEveryMailbox(t *testing.T) {
	sock := sockPath(t)
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", sock)
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "child-token")
	resetWakeStreams()
	t.Cleanup(resetWakeStreams)
	t.Cleanup(func() { recordWakePending(false, "") })
	lines := listenLines(t, sock)
	var reads atomic.Int32
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
		if req.Method == "resources/read" {
			reads.Add(1)
			text := "fresh-" + req.Params.Meta["com.dibs/token"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": "dibs-wake-refresh",
				"result": map[string]any{"contents": []map[string]any{{"text": text}}},
			})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/subscriptions/acknowledged\"}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); srv.Close() })
	blob, err := json.Marshal(bridgeState{WakeStreams: []wakeHandoff{
		{Key: "legacy", Token: "legacy-mailbox", Since: 7},
		{Key: "capable", Token: "capable-mailbox", Since: 8, Refresh: true},
	}, WakePending: true, WakeNotice: "captured-legacy-text", Thread: streamSession()})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(bridgeStateEnv, string(blob))
	var iw inboxWatcher
	var streams sync.WaitGroup
	restoreCarried(ctx, srv.Client(), srv.URL, "secret", &syncWriter{w: bufio.NewWriter(io.Discard)}, &streams, &iw, true, shipTiming{})
	got := strings.Join(collect(lines, 2, time.Second), "\n")
	if reads.Load() != 2 || !strings.Contains(got, "fresh-legacy-mailbox") || !strings.Contains(got, "fresh-capable-mailbox") || strings.Contains(got, "captured-legacy-text") {
		t.Fatalf("mixed handoff dropped a mailbox or reused old text: reads=%d, wire=%s", reads.Load(), got)
	}
}

func TestRestoredSelfWakeRefreshesBeforeDeliveringCapturedText(t *testing.T) {
	sock := sockPath(t)
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", sock)
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "child-token")
	resetWakeStreams()
	t.Cleanup(resetWakeStreams)
	t.Cleanup(func() { recordWakePending(false, "") })
	lines := listenLines(t, sock)
	var reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Method == "resources/read" {
			reads.Add(1)
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":"dibs-wake-refresh","result":{"contents":[{"text":""}]}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/subscriptions/acknowledged\"}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); srv.Close() })
	blob, err := json.Marshal(bridgeState{
		WakeStreams: []wakeHandoff{{Key: "worker", Token: "tok", Since: 7, Refresh: true}},
		WakePending: true, WakeNotice: "stale-digest-marker", Thread: streamSession(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(bridgeStateEnv, string(blob))
	var iw inboxWatcher
	var streams sync.WaitGroup
	restoreCarried(ctx, srv.Client(), srv.URL, "secret", &syncWriter{w: bufio.NewWriter(io.Discard)}, &streams, &iw, true, shipTiming{})
	if reads.Load() != 1 {
		t.Fatalf("restore bypassed its carried refresh capability: %d reads", reads.Load())
	}
	if got := collect(lines, 2, 100*time.Millisecond); len(got) != 0 {
		t.Fatalf("restore sent old text after an empty fresh read: %v", got)
	}
	w := iw.sharedWaker()
	w.mu.Lock()
	spent := !w.last.IsZero()
	w.mu.Unlock()
	if spent {
		t.Fatal("an empty fresh read spent the cooldown")
	}
}

func TestFreshEmptySettlesARetryWithoutWritingOrSurrendering(t *testing.T) {
	t.Cleanup(func() { recordWakePending(false, "") })
	var calls atomic.Int32
	w := &selfWaker{
		cooldown: 20 * time.Millisecond,
		refreshFn: func(captured string) (string, error) {
			if calls.Add(1) == 1 {
				return "", errors.New("temporary board failure")
			}
			return "", nil
		},
		deliverFn: func(string) error { t.Error("an empty or failed fresh read reached the socket writer"); return nil },
	}
	if err := w.wake("stale-digest-marker"); err == nil {
		t.Fatal("a failed fresh read reported delivery")
	}
	for deadline := time.Now().Add(time.Second); ; {
		w.mu.Lock()
		pending, retry, last := w.pending, w.retry, w.last
		w.mu.Unlock()
		if calls.Load() > 1 && !pending && !retry && last.IsZero() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fresh empty retry was not settled or spent the cooldown")
		}
		time.Sleep(time.Millisecond)
	}
	if !w.canReach() {
		t.Fatal("a settled empty retry surrendered a working socket")
	}
	if pending, _ := currentWakePending(); pending {
		t.Fatal("settled retry left stale upgrade handoff state")
	}
}
