// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
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

func TestHostBridgePreInputFailureUsesColdRouteWithFreshness(t *testing.T) {
	for _, owed := range []bool{false, true} {
		t.Run(map[bool]string{false: "handled", true: "owed"}[owed], func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("DIBS_DIR", dir)
			if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("fixture"), 0o600); err != nil {
				t.Fatal(err)
			}
			s := testcodexipc.Start(t, "changed", nil)
			checks := 0
			hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/wake-owed" || r.Header.Get("X-Dibs-Local") != "fixture" {
					t.Error("missing real authenticated freshness path")
					w.WriteHeader(401)
					return
				}
				checks++
				_ = json.NewEncoder(w).Encode(map[string]bool{"owed": owed})
			}))
			t.Cleanup(hub.Close)
			b := newWakeBridge(hub.URL, "fixture", "local", map[string]boardconfig.WakeExec{
				"codex": {Argv: []string{"codex", "queue", "--thread", "{thread}", "--message", "{message}"}},
			})
			queued := 0
			b.run = func(argv, _ []string, _, _ string, _, _ time.Duration) bool {
				queued++
				if len(argv) != 6 || argv[5] != "Dibs: a new request is waiting." {
					t.Errorf("private facts entered argv: %v", argv)
				}
				return true
			}
			ok, _, out := b.executeOutcome(engine.WakeRequest{
				ID: 1, Host: "local", Agent: "worker", Harness: "codex", Thread: testcodexipc.Thread,
				Surface: harnessenv.ChatGPTApp, From: "sender", MsgType: "request",
			})
			want := 0
			if owed {
				want = 1
			}
			if !ok || queued != want || checks != 1 || len(s.Inputs()) != 0 || out.NoRetry {
				t.Fatalf("cold fallback bypassed freshness or stranded mail: ok=%v queue=%d checks=%d out=%+v", ok, queued, checks, out)
			}
		})
	}
}
