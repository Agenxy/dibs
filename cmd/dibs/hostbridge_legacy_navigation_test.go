// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/boardconfig"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/testcodexipc"
)

// The older binding omits surface; the receiver's actual rollout header is
// the app evidence. Exercise execution and visibility through their real doors.
func TestLegacyNativeOnlyBridgePreservesOwnedInputWithoutNavigation(t *testing.T) {
	for _, mode := range []string{"idle", "no-owner", "app-open"} {
		t.Run(mode, func(t *testing.T) {
			dir := cliIPCProfile(t)
			t.Setenv("DIBS_DIR", dir)
			if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("fixture"), 0o600); err != nil {
				t.Fatal(err)
			}
			nativeMode := mode
			if mode == "app-open" {
				nativeMode = "no-owner"
			}
			s := testcodexipc.Start(t, nativeMode, nil)
			sessions := filepath.Join(os.Getenv("CODEX_HOME"), "sessions")
			if err := os.Mkdir(sessions, 0o700); err != nil {
				t.Fatal(err)
			}
			header := `{"type":"session_meta","payload":{"id":"` + testcodexipc.Thread + `","originator":"Codex Desktop"}}`
			if err := os.WriteFile(filepath.Join(sessions, "rollout-fixture-"+testcodexipc.Thread+".jsonl"), []byte(header), 0o600); err != nil {
				t.Fatal(err)
			}
			if harnessenv.AppFor("", "Codex", testcodexipc.Thread) != harnessenv.ChatGPTApp {
				t.Fatal("setup: legacy app origin not established")
			}
			hub := newLocalTestServer(t, dir, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]bool{"owed": true})
			}))
			t.Cleanup(hub.Close)
			b := newWakeBridge(hub.URL, "fixture", "local", map[string]boardconfig.WakeExec{"codex": {Argv: []string{"codex", "queue", "--thread", "{thread}", "--message", "{message}"}}})
			queued, opened := 0, 0
			b.run = func(_, _ []string, _, _ string, _, _ time.Duration) bool { queued++; return true }
			b.show = harnessenv.Shower{Ownership: func(string) harnessenv.ThreadOwnership { return harnessenv.ThreadOwnership{Running: true, Known: true} }, Open: func([]string) error { opened++; return nil }}
			var wr engine.WakeRequest
			if err := json.Unmarshal([]byte(`{"id":1,"host":"local","agent":"worker","harness":"codex","thread":"`+testcodexipc.Thread+`","msg_type":"notify","no_navigation":true}`), &wr); err != nil {
				t.Fatal(err)
			}
			if mode == "app-open" {
				b.showQueuedThread(wr)
			} else {
				_, _, _ = b.executeOutcome(wr)
			}
			want := 0
			if mode == "idle" {
				want = 1
			}
			if queued != 0 || opened != 0 || len(s.Inputs()) != want {
				t.Fatalf("legacy native-only input=%d want=%d queued=%d opened=%d", len(s.Inputs()), want, queued, opened)
			}
		})
	}
}
