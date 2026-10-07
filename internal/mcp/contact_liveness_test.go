// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
)

type contactProber struct{ alive atomic.Bool }

func (p *contactProber) Alive(int) bool { return p.alive.Load() }

// Real authenticated model calls, not a test-set freshness flag. The silent
// sibling proves the production sweep has actually run and still detects death.
func TestAuthenticatedMCPContactBeatsAStaleRecordedPID(t *testing.T) {
	for _, method := range []string{"inbox", "check_in"} {
		t.Run(method, func(t *testing.T) {
			dir := t.TempDir()
			box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
			if err != nil {
				t.Fatal(err)
			}
			journal, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "contact", box)
			if err != nil {
				t.Fatal(err)
			}
			prober := &contactProber{}
			prober.alive.Store(true)
			eng := engine.New(core.NewState("contact", core.DefaultLimits()), journal, prober)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { eng.Run(ctx); close(done) }()
			srv := httptest.NewServer(New(eng))
			t.Cleanup(func() { srv.Close(); cancel(); <-done; _ = journal.Close() })
			register := func(name string, pid int) string {
				r := toolCall(t, srv, "register", map[string]any{
					"name": name, "nonce": "contact-nonce-" + name, "kind": "persistent",
					"pid": pid, "harness": "codex", "session_id": "codex-no-sidecar-" + name,
				})
				token, ok := r["token"].(string)
				if !ok || r["__is_error"] == true {
					t.Fatal("register setup did not succeed")
				}
				return token
			}
			fresh := register("fresh", 424242)
			_ = register("silent", 424243)
			contact := toolCall(t, srv, method, map[string]any{"token": fresh})
			if contact["__is_error"] == true {
				t.Fatal("authenticated contact setup was refused")
			}
			events, unsubscribe := eng.Subscribe(0)
			defer func() {
				if unsubscribe != nil {
					unsubscribe()
				}
			}()
			prober.alive.Store(false)
			var rows map[string]map[string]any
			deadline := time.NewTimer(10 * time.Second)
			defer deadline.Stop()
			for {
				b, err := eng.Board(ctx) // passive observation, never agent-token contact
				if err != nil {
					t.Fatal(err)
				}
				rows = map[string]map[string]any{}
				for _, row := range b["agents"].([]map[string]any) {
					rows[row["id"].(string)] = row
				}
				if rows["silent"]["status"] == core.StatusDormant {
					break
				}
				select {
				case <-events:
				case <-deadline.C:
					t.Fatal("setup/control: no production crash sweep within deadline")
				}
			}
			if rows["silent"]["status"] != core.StatusDormant ||
				rows["silent"]["stale_reason"] != "process_exited" {
				t.Fatal("setup/control: actual sweep did not detect the silent dead process")
			}
			if rows["fresh"]["status"] != core.StatusActive {
				t.Fatal("actual sweep let a stale PID defeat fresh authenticated MCP contact")
			}
			if rows["fresh"]["proc_alive"] != false {
				t.Fatal("fresh identity contact was misreported as a living recorded process")
			}
			// Replay the encrypted production ledger, so the observed decision
			// cannot depend on probing this machine again during the fold.
			unsubscribe()
			unsubscribe = nil
			srv.Close()
			cancel()
			<-done
			replayed := core.NewState("contact", core.DefaultLimits())
			if _, err := journal.Replay(replayed); err != nil {
				t.Fatal(err)
			}
			if replayed.Agents["fresh"].Status != core.StatusActive ||
				replayed.Agents["silent"].StaleReason != "process_exited" {
				t.Fatal("recorded sweep decision changed on replay")
			}
		})
	}
}
