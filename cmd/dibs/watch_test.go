// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
)

// agenxy-supply, 2026-10-11: `dibs watch --types notify` fired on serial
// 89473, a notify it had SENT, because await_events also returns events the
// caller did. A self-wake watcher then re-fired on every send it made. Through
// the real event path, as the watcher reads it after the JSON round trip.
func TestWatchFiresOnMailToTheWatcherAndNotOnItsOwnSends(t *testing.T) {
	dir := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "watch", box)
	if err != nil {
		t.Fatal(err)
	}
	e := engine.New(core.NewState("watch", core.DefaultLimits()), led, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done; _ = led.Close() })
	token := map[string]string{}
	for _, n := range []string{"supply", "dev"} {
		r, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: n})
		if err != nil {
			t.Fatal("setup:", err)
		}
		token[n] = r["token"].(string)
		if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: token[n]}); err != nil {
			t.Fatal("setup:", err)
		}
	}
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: token["supply"], To: "dev", MsgType: core.MsgNotify, Body: "done",
	}); err != nil {
		t.Fatal("setup send:", err)
	}
	fires := func(watcher string) int {
		res, err := e.AwaitEvents(ctx, token[watcher], 0, time.Second, false)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]any
		b, _ := json.Marshal(res)
		_ = json.Unmarshal(b, &wire)
		n, seen := 0, 0
		evs, _ := wire["events"].([]any)
		for _, raw := range evs {
			ev, _ := raw.(map[string]any)
			if ev["type"] != "message.sent" {
				continue
			}
			seen++
			if mailForWatcher(ev, wire) {
				n++
			}
		}
		if seen == 0 {
			t.Fatalf("setup: %s saw no message.sent at all, so this proves nothing", watcher)
		}
		return n
	}
	if n := fires("supply"); n != 0 {
		t.Errorf("the sender's watcher fired %d time(s) on its own send", n)
	}
	if n := fires("dev"); n != 1 {
		t.Errorf("the recipient's watcher fired %d time(s), want 1", n)
	}
}
