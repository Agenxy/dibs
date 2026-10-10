// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/testcodexipc"
)

// Enter through authenticated MCP send and the real background native adapter.
// Holding owner discovery proves that returning a pending receipt neither waits
// for app acceptance nor invents it. The same guard runs on the old production
// code without seeding an observation cache or calling a presentation helper.
func TestNativeSendReceiptNamesPendingRouteAndPreservesPriorOutcome(t *testing.T) {
	for _, mode := range []string{"idle", "active", "disconnect", "refused"} {
		t.Run(mode, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var seen, freed sync.Once
			app := testcodexipc.Start(t, mode, func() {
				seen.Do(func() { close(entered) })
				<-release
			})
			srv, eng, _ := newServerWithEngine(t)
			t.Cleanup(func() { freed.Do(func() { close(release) }) })
			eng.SetWakeCommands(map[string]engine.WakeCommand{"codex": {Argv: []string{
				filepath.Join(t.TempDir(), "codex"), "queue", "--thread", "{thread}", "--message", "{message}",
			}}})
			registerObservationAppWorker(t, srv, eng, testcodexipc.Thread, t.TempDir())
			sender := toolCall(t, srv, "register", map[string]any{"name": "receipt-sender"})
			if sender["token"] == nil || sender["code"] != nil {
				t.Fatalf("setup sender: %v", sender)
			}
			send := func() map[string]any {
				return toolCall(t, srv, "send", map[string]any{
					"token": sender["token"], "to": "queue-worker", "type": "notify", "body": "private receipt marker",
				})
			}
			replies := make(chan map[string]any, 1)
			go func() { replies <- send() }()
			var first map[string]any
			select {
			case first = <-replies:
			case <-time.After(3 * time.Second):
				t.Fatal("send waited for native delivery instead of returning its receipt")
			}
			if first["msg_serial"] == nil || first["code"] != nil || first["__is_error"] == true {
				t.Fatalf("setup first send: %v", first)
			}
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("setup: production wake never reached native owner discovery")
			}
			if len(app.Inputs()) != 0 {
				t.Fatal("setup: app input passed the held discovery")
			}
			view, ok := first["queue_wake"].(map[string]any)
			if !ok || view["native_delivery"] != "in_progress" || view["observed_at"] != nil {
				t.Errorf("send reported a queue observation or acceptance before native delivery: %v", first)
			}
			note, _ := first["note"].(string)
			if !strings.Contains(note, "Direct delivery in progress; check the board for the outcome.") ||
				strings.Contains(note, "App queue admission:") {
				t.Errorf("pending native send named the wrong route: %s", note)
			}
			freed.Do(func() { close(release) })
			want := "accepted"
			switch mode {
			case "disconnect":
				want = "unknown"
			case "refused":
				want = "not_sent"
			}
			until := time.Now().Add(3 * time.Second)
			for {
				board, err := eng.Board(context.Background())
				if err != nil {
					t.Fatal("setup board:", err)
				}
				found := false
				for _, row := range board["agents"].([]map[string]any) {
					q, _ := row["queue_wake"].(core.Result)
					if row["id"] == "queue-worker" && q["native_delivery"] == want {
						found = true
					}
				}
				if found {
					break
				}
				if time.Now().After(until) {
					t.Fatalf("setup: actual native outcome %s never reached the board", want)
				}
				<-time.After(5 * time.Millisecond)
			}
			second := send()
			if second["msg_serial"] == nil || second["code"] != nil || second["__is_error"] == true {
				t.Fatalf("setup second send: %v", second)
			}
			prior, ok := second["queue_wake"].(map[string]any)
			if !ok || prior["native_delivery"] != want || prior["observed_at"] == nil {
				t.Errorf("new send lost the prior native outcome: %v", second)
			}
			note, _ = second["note"].(string)
			if !strings.Contains(note, "Last native app") || strings.Contains(note, "Direct delivery in progress") {
				t.Errorf("prior outcome was presented as this send's new acceptance: %s", note)
			}
			raw, err := json.Marshal(prior)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), testcodexipc.Thread) || strings.Contains(string(raw), "private receipt marker") {
				t.Errorf("send observation leaked thread or mail: %s", raw)
			}
		})
	}
}

func TestNativeSendReceiptDoesNotInventSuppressedAttempt(t *testing.T) {
	app := testcodexipc.Start(t, "idle", nil)
	srv, eng, _ := newServerWithEngine(t)
	eng.SetWakePolicy(engine.WakeNone)
	eng.SetWakeCommands(map[string]engine.WakeCommand{"codex": {Argv: []string{
		filepath.Join(t.TempDir(), "codex"), "queue", "--thread", "{thread}", "--message", "{message}",
	}}})
	registerObservationAppWorker(t, srv, eng, testcodexipc.Thread, t.TempDir())
	sender := toolCall(t, srv, "register", map[string]any{"name": "suppressed-sender"})
	if sender["token"] == nil || sender["code"] != nil {
		t.Fatalf("setup sender: %v", sender)
	}
	receipt := toolCall(t, srv, "send", map[string]any{
		"token": sender["token"], "to": "queue-worker", "type": "notify", "body": "suppressed mail",
	})
	if receipt["msg_serial"] == nil || receipt["code"] != nil || receipt["__is_error"] == true {
		t.Fatalf("setup suppressed send: %v", receipt)
	}
	view, ok := receipt["queue_wake"].(map[string]any)
	note, _ := receipt["note"].(string)
	if !ok || view["native_delivery"] != nil || !strings.Contains(note, "wake policy suppresses") || len(app.Inputs()) != 0 {
		t.Fatalf("suppressed mail acquired a native attempt: %v", receipt)
	}
}
