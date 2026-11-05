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
	"github.com/agenxy/dibs/internal/wakeexec"
)

func TestHostBridgeNativeOnlyPlanNeverFallsBackOrNavigates(t *testing.T) {
	for _, door := range []string{"native-refusal", "app-open"} {
		t.Run(door, func(t *testing.T) {
			dir := cliIPCProfile(t)
			t.Setenv("DIBS_DIR", dir)
			if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("fixture"), 0o600); err != nil {
				t.Fatal(err)
			}
			testcodexipc.Start(t, "no-owner", nil)
			hub := newLocalTestServer(t, dir, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]bool{"owed": true})
			}))
			t.Cleanup(hub.Close)
			b := newWakeBridge(hub.URL, "fixture", "local", map[string]boardconfig.WakeExec{"codex": {Argv: []string{"codex", "queue", "--thread", "{thread}", "--message", "{message}"}}})
			queued, opened := 0, 0
			b.run = func(_, _ []string, _, _ string, _, _ time.Duration) wakeexec.CommandOutcome {
				queued++
				return wakeexec.CommandOutcome{OK: true}
			}
			b.show = harnessenv.Shower{Ownership: func(string) harnessenv.ThreadOwnership { return harnessenv.ThreadOwnership{Running: true, Known: true} }, Open: func([]string) error { opened++; return nil }}
			var request engine.WakeRequest
			// A wire fixture lets this guard run against the old reader: ignoring the
			// additive flag must fail behaviorally, not fail to compile.
			if err := json.Unmarshal([]byte(`{"id":1,"host":"local","agent":"worker","harness":"codex","thread":"`+testcodexipc.Thread+`","surface":"chatgpt-app","msg_type":"notify","no_navigation":true}`), &request); err != nil {
				t.Fatal(err)
			}
			if door == "native-refusal" {
				_, _, _ = b.executeOutcome(request)
			} else {
				b.showQueuedThread(request)
			}
			if queued != 0 || opened != 0 {
				t.Fatalf("native-only plan queued=%d opened=%d", queued, opened)
			}
		})
	}
}
