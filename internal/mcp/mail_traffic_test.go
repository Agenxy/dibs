// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
	"github.com/agenxy/dibs/internal/web"
)

func trafficDays(t *testing.T, r map[string]any) []any {
	t.Helper()
	v, ok := r["traffic"].(map[string]any)
	if !ok {
		t.Fatalf("traffic projection absent: %v", r)
	}
	rows, ok := v["days"].([]any)
	if !ok {
		t.Fatalf("traffic days absent: %v", v)
	}
	return rows
}

func TestMailTrafficSelfOnlyAndCoordinatorThroughBothProtocols(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, eng, _ := newServerWithEngine(t)
			call := func(t *testing.T, name string, args map[string]any) map[string]any {
				return toolCallOn(t, srv, version, name, args)
			}
			member := call(t, "register", map[string]any{"name": "traffic-member"})["token"].(string)
			other := call(t, "register", map[string]any{"name": "traffic-other"})["token"].(string)
			coord := call(t, "register", map[string]any{"name": "traffic-coord"})["token"].(string)
			if _, err := eng.Do(context.Background(), &core.Op{Kind: core.OpGrantRole, To: "traffic-coord", Mode: core.RoleCoordinator}); err != nil {
				t.Fatal(err)
			}
			for _, kind := range []string{"notify", "question", "request", "handoff"} {
				r := call(t, "send", map[string]any{"token": member, "to": "traffic-other", "type": kind, "body": "PRIVATE COUNT BODY", "op_id": "traffic-" + kind})
				if r["ok"] != true {
					t.Fatalf("setup send: %v", r)
				}
				r = call(t, "send", map[string]any{"token": member, "to": "traffic-other", "type": kind, "body": "PRIVATE COUNT BODY", "op_id": "traffic-" + kind})
				if r["deduplicated"] != true {
					t.Fatalf("setup dedup: %v", r)
				}
			}
			t.Run("own", func(t *testing.T) {
				days := trafficDays(t, call(t, "all_mail", map[string]any{"token": member, "census": true, "traffic": true}))
				if len(days) != 1 {
					t.Fatalf("days: %v", days)
				}
				rows := days[0].(map[string]any)["agents"].([]any)
				if len(rows) != 1 || rows[0].(map[string]any)["agent"] != "traffic-member" {
					t.Fatalf("cross-agent leak: %v", rows)
				}
				sent := rows[0].(map[string]any)["sent"].(map[string]any)
				for _, kind := range []string{"notify", "question", "request", "handoff"} {
					if sent[kind] != float64(1) {
						t.Fatalf("%s counted retry: %v", kind, sent)
					}
				}
			})
			t.Run("other-refused", func(t *testing.T) {
				r := call(t, "all_mail", map[string]any{"token": member, "census": true, "traffic": true, "agent": "traffic-other"})
				if r["code"] != "E_NOT_COORDINATOR" {
					t.Fatalf("member read peer: %v", r)
				}
			})
			t.Run("coordinator", func(t *testing.T) {
				r := call(t, "all_mail", map[string]any{"token": coord, "census": true, "traffic": true, "agent": "traffic-other"})
				days := trafficDays(t, r)
				rows := days[0].(map[string]any)["agents"].([]any)
				got := rows[0].(map[string]any)["asks_received"].(map[string]any)
				if len(rows) != 1 || got["question"] != float64(1) || got["request"] != float64(1) || got["handoff"] != float64(1) || got["notify"] != nil {
					t.Fatalf("asks: %v", rows)
				}
				b, _ := json.Marshal(r)
				if strings.Contains(string(b), "PRIVATE COUNT BODY") {
					t.Fatal("body in counts")
				}
			})
			t.Run("board-totals", func(t *testing.T) {
				w, err := web.New(eng)
				if err != nil {
					t.Fatal(err)
				}
				mux := http.NewServeMux()
				w.Register(mux)
				out := httptest.NewRecorder()
				mux.ServeHTTP(out, httptest.NewRequest("GET", "/api/traffic", nil))
				var r map[string]any
				if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &r) != nil {
					t.Fatalf("totals endpoint: %d %s", out.Code, out.Body.String())
				}
				days := trafficDays(t, r)
				day := days[0].(map[string]any)
				if day["sent"].(map[string]any)["notify"] != float64(1) {
					t.Fatalf("totals: %v", day)
				}
				if strings.Contains(out.Body.String(), "traffic-member") || strings.Contains(out.Body.String(), "PRIVATE COUNT BODY") || day["agents"] != nil {
					t.Fatal("totals exposed per-agent detail")
				}
			})
			_ = other
		})
	}
}

func trafficRestartServer(t *testing.T, dir string) (*httptest.Server, *engine.Engine, *core.State, bool, func()) {
	t.Helper()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "traffic", box)
	if err != nil {
		t.Fatal(err)
	}
	journal.KeepHistory(time.Nanosecond)
	lim := core.DefaultLimits()
	lim.ConsumedRetention = 0
	st := core.NewState("traffic", lim)
	if _, err = journal.Replay(st); err != nil {
		t.Fatal(err)
	}
	_, anchored := journal.Anchor()
	eng := engine.New(st, journal, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { eng.Run(ctx); close(done) }()
	srv := httptest.NewServer(New(eng))
	var once sync.Once
	stop := func() {
		once.Do(func() {
			srv.Close()
			cancel()
			<-done
			if err := journal.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	t.Cleanup(stop)
	return srv, eng, st, anchored, stop
}

func TestMailTrafficSurvivesMessagePruneAndEncryptedSnapshot(t *testing.T) {
	dir := t.TempDir()
	srv, eng, st, _, stop := trafficRestartServer(t, dir)
	sender := toolCall(t, srv, "register", map[string]any{"name": "counter-sender"})["token"].(string)
	reader := toolCall(t, srv, "register", map[string]any{"name": "counter-reader"})["token"].(string)
	coord := toolCall(t, srv, "register", map[string]any{"name": "counter-coord"})["token"].(string)
	if _, err := eng.Do(context.Background(), &core.Op{Kind: core.OpGrantRole, To: "counter-coord", Mode: core.RoleCoordinator}); err != nil {
		t.Fatal(err)
	}
	sent := toolCall(t, srv, "send", map[string]any{"token": sender, "to": "counter-reader", "type": "notify", "body": "PRIVATE PRUNED TRAFFIC"})
	if sent["ok"] != true {
		t.Fatalf("setup send: %v", sent)
	}
	ack := toolCall(t, srv, "ack", map[string]any{"token": reader, "msg_serial": sent["msg_serial"]})
	if ack["ok"] != true {
		t.Fatalf("setup ack: %v", ack)
	}
	if _, err := eng.Do(context.Background(), &core.Op{Kind: core.OpSweep}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Prune(context.Background(), "counter-sender"); err != nil {
		t.Fatal(err)
	}
	stop()
	if len(st.Messages) != 0 {
		t.Fatalf("setup: mail survived sweep: %v", st.Messages)
	}
	_, _, _, anchored, stop := trafficRestartServer(t, dir)
	if !anchored {
		t.Fatal("setup: no snapshot anchor, compaction was not exercised")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 0 {
		t.Fatalf("setup: sends still in ledger after snapshot: %s", raw)
	}
	// The first reopen wrote the compacted snapshot while still holding its
	// replayed memory. Reopen AGAIN with the send prefix gone to prove decoding.
	stop()
	srv, _, _, anchored, stop = trafficRestartServer(t, dir)
	if !anchored {
		t.Fatal("setup: compacted snapshot was not loaded")
	}
	r := toolCall(t, srv, "all_mail", map[string]any{"token": coord, "census": true, "traffic": true, "agent": "counter-sender"})
	days := trafficDays(t, r)
	if len(days) != 1 {
		t.Fatalf("snapshot lost days: %v", days)
	}
	rows := days[0].(map[string]any)["agents"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["sent"].(map[string]any)["notify"] != float64(1) {
		t.Fatalf("snapshot lost counts: %v", rows)
	}
	stop()
}
