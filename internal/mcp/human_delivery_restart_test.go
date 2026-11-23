// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/humanask"
	"github.com/agenxy/dibs/internal/ledger"
)

type restartDesktopFixture struct {
	desktopFixture
	alreadyPosted uint64
}

func (f restartDesktopFixture) MessageDelivered(_ context.Context, node string, serial uint64) bool {
	return node == "restart-board" && serial == f.alreadyPosted
}

func TestHumanDesktopDeliverySurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	start := func(configure bool, alreadyPosted uint64) (*httptest.Server, <-chan humanask.Message, func()) {
		t.Helper()
		led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "restart-board", box)
		if err != nil {
			t.Fatal(err)
		}
		st := core.NewState("restart-board", core.DefaultLimits())
		if _, err := led.Replay(st); err != nil {
			t.Fatal(err)
		}
		eng := engine.New(st, led, nil)
		if configure {
			if err := eng.SetHumanPostingStore(dir); err != nil {
				t.Fatal(err)
			}
		}
		attempts := make(chan humanask.Message, 4)
		eng.SetHumanNotifier(restartDesktopFixture{
			alreadyPosted: alreadyPosted,
			desktopFixture: desktopFixture{
				available: true,
				ask: func(m humanask.Message) (humanask.Answer, error) {
					attempts <- m
					return humanask.Answer{}, nil
				},
			},
		})
		ctx, cancel := context.WithCancel(context.Background())
		go eng.Run(ctx)
		srv := httptest.NewServer(New(eng))
		stop := sync.OnceFunc(func() {
			srv.Close()
			cancel()
			<-eng.Done()
			if err := led.Close(); err != nil {
				t.Error(err)
			}
		})
		t.Cleanup(stop)
		return srv, attempts, stop
	}
	attempt := func(attempts <-chan humanask.Message, serial uint64) humanask.Message {
		t.Helper()
		select {
		case m := <-attempts:
			if m.Serial != serial || m.Node != "restart-board" {
				t.Fatalf("desktop submission identity: board %q, serial %d, want restart-board %d", m.Node, m.Serial, serial)
			}
			return m
		case <-time.After(4 * time.Second):
			t.Fatalf("desktop did not submit live ask %d", serial)
			return humanask.Message{}
		}
	}
	before, attempts, stop := start(false, 0)
	reg := toolCall(t, before, "register", map[string]any{"name": "sender", "nonce": "restart-fixture"})
	token, ok := reg["token"].(string)
	if !ok {
		t.Fatalf("register did not issue a token: %v", reg)
	}
	send := func(body string) uint64 {
		t.Helper()
		res := toolCall(t, before, "send", map[string]any{
			"token": token, "to": "human", "type": "request", "body": body,
		})
		serial, ok := res["msg_serial"].(float64)
		if !ok || res["human_route"] != "desktop" {
			t.Fatalf("send did not choose desktop: %v", res)
		}
		return uint64(serial)
	}
	read := func(srv *httptest.Server, serial uint64) (map[string]any, map[string]any) {
		t.Helper()
		res := toolCall(t, srv, "read_mail", map[string]any{
			"token": token, "msg_serial": float64(serial), "include_body": true,
		})
		return asMap(res["message"]), asMap(res["human_delivery"])
	}
	legacy := send("An ask retained from before the upgrade checkpoint")
	attempt(attempts, legacy)
	stop()

	before, attempts, stop = start(true, 0)
	posted := send("A desktop post confirmed before restart")
	attempt(attempts, posted).Receipt("posted")
	if _, delivery := read(before, posted); delivery["posted"] != true {
		t.Fatalf("setup did not record OS posting: %v", delivery)
	}
	alreadyPosted := send("A desktop post still present without its receipt")
	attempt(attempts, alreadyPosted)
	unconfirmed := send("A desktop post interrupted before its receipt")
	attempt(attempts, unconfirmed)
	stop()

	after, restored, stop := start(true, alreadyPosted)
	message, delivery := read(after, posted)
	if delivery["posted"] != true || delivery["route"] != "desktop" {
		t.Errorf("confirmed desktop posting evidence lost at restart: %v", delivery)
	}
	if _, ok := message["undelivered_for_seconds"]; ok {
		t.Error("OS acceptance must not be projected as undelivered human mail")
	}
	if pending, _ := read(after, unconfirmed); pending["state"] != core.MsgStatePending {
		t.Fatalf("setup did not retain the still-open ask: %v", pending)
	}
	legacyMessage, legacyDelivery := read(after, legacy)
	hint, _ := legacyDelivery["hint"].(string)
	if legacyDelivery["state"] != "unknown" || !strings.Contains(hint, "not known since restart") ||
		legacyMessage["undelivered_for_seconds"] != nil {
		t.Errorf("an ask predating the recovery checkpoint must report unknown posting: %v %v", legacyMessage, legacyDelivery)
	}
	attempt(restored, unconfirmed)
	if _, delivery := read(after, alreadyPosted); delivery["posted"] != true || delivery["route"] != "desktop" {
		t.Errorf("a keyed notice still present in Notification Center supplies posting evidence: %v", delivery)
	}
	stop()

	if err := os.Remove(filepath.Join(dir, "human-posting-receipts.json")); err != nil {
		t.Fatal(err)
	}
	_, lost, _ := start(true, 0)
	attempt(lost, posted)
}
