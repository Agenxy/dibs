// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/boardconfig"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/testcodexipc"
)

func TestHostBridgeNativeDeliveryDoesNotQueue(t *testing.T) {
	for _, mode := range []string{"idle", "active", "disconnect", "no-owner", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("DIBS_DIR", dir)
			if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("fixture"), 0o600); err != nil {
				t.Fatal(err)
			}
			s := testcodexipc.Start(t, mode, nil)
			reports := make(chan engine.WakeResult, 1)
			hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Dibs-Local") != "fixture" {
					t.Error("missing board credential")
					w.WriteHeader(401)
					return
				}
				if r.URL.Path == "/api/wake-owed" {
					var req struct {
						ID   uint64
						Host string
					}
					if json.NewDecoder(r.Body).Decode(&req) != nil || req.ID != 1 || req.Host != "local" {
						t.Error("wrong freshness fence")
					}
					_ = json.NewEncoder(w).Encode(map[string]bool{"owed": mode != "cancelled"})
					return
				}
				var res engine.WakeResult
				if json.NewDecoder(r.Body).Decode(&res) != nil {
					t.Error("bad report")
				}
				reports <- res
				_ = json.NewEncoder(w).Encode(map[string]bool{"accepted": true})
			}))
			t.Cleanup(hub.Close)
			b := newWakeBridge(hub.URL, "fixture", "local", map[string]boardconfig.WakeExec{
				"codex": {Argv: []string{"codex", "queue", "--thread", "{thread}", "--message", "{message}"}},
			})
			queued := 0
			b.run = func(_, _ []string, _, _ string, _, _ time.Duration) bool { queued++; return true }
			wr := engine.WakeRequest{
				ID: 1, Host: "local", Agent: "worker", Harness: "codex", Thread: testcodexipc.Thread,
				Surface: harnessenv.ChatGPTApp, From: "sender", MsgType: "request", Notice: "hub imperative rejected",
			}
			if mode == "no-owner" {
				ok, detail := b.execute(wr)
				if !ok || queued != 1 || detail != "thread not loaded in the app; queued until opened" {
					t.Fatalf("cold route: %v %d %s", ok, queued, detail)
				}
				return
			}
			// A previous command cannot hold a new native input. The app admits it.
			b.engaged(99, "worker")
			ctx, cancel := context.WithCancel(context.Background())
			joined := make(chan struct{})
			go func() { b.reporter(ctx); close(joined) }()
			t.Cleanup(func() { cancel(); <-joined })
			b.dispatch(ctx, wr)
			var res engine.WakeResult
			select {
			case res = <-reports:
			case <-time.After(3 * time.Second):
				t.Fatal("no native report")
			}
			want := 1
			if mode == "cancelled" {
				want = 0
			}
			if queued != 0 || len(s.Inputs()) != want {
				t.Fatalf("native route queued or retried: %d %d", queued, len(s.Inputs()))
			}
			if res.OK != (mode != "disconnect") {
				t.Fatalf("native acceptance: %+v", res)
			}
		})
	}
}
