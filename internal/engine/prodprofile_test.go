// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/ledger"
)

// TestProfileARealBoard measures the read and check_in paths against a copy
// of a real data directory, because a synthetic board does not have the shape
// that makes a real one slow: hundreds of archived rows, thousands of settled
// messages, months of ledger. DIBS_PROD_STATE names a directory holding key,
// node_id, ledger.jsonl and snapshot.dibs; it is copied first, so the original
// is never opened for writing. Every wake route is off and HOME, the socket
// directory and CODEX_HOME are redirected, so nothing reaches a live session.
func TestProfileARealBoard(t *testing.T) {
	src := os.Getenv("DIBS_PROD_STATE")
	if src == "" {
		t.Skip("DIBS_PROD_STATE not set")
	}
	tmp := t.TempDir()
	for _, env := range []string{"HOME", "XDG_RUNTIME_DIR", "CODEX_HOME", "CLAUDE_CODE_TMPDIR"} {
		t.Setenv(env, tmp)
	}
	t.Setenv("DIBS_TEST_FORBID_APP_OPEN", "1")
	dir := filepath.Join(tmp, "data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"key", "node_id", "ledger.jsonl", "snapshot.dibs"} {
		copyFile(t, filepath.Join(src, f), filepath.Join(dir, f))
	}
	node, err := os.ReadFile(filepath.Join(dir, "node_id"))
	if err != nil {
		t.Fatal(err)
	}
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), string(node), box)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = led.Close() }()
	limits := core.DefaultLimits()
	limits.MaxAgents, limits.MaxPersistentAgents = 128, 128
	st := core.NewState(string(node), limits)
	var history []core.Event
	led.OnEvents = func(evs []core.Event) { history = append(history, evs...) }
	start := time.Now()
	n, err := led.Replay(st)
	if err != nil {
		t.Fatal(err)
	}
	led.OnEvents = nil
	t.Logf("replay: %d ops in %v; agents %d, messages %d, claims %d, events %d",
		n, time.Since(start), len(st.Agents), len(st.Messages), len(st.Claims), len(history))
	start = time.Now()
	e := New(st, led, pidOnlyProber{}, history)
	t.Logf("engine.New: %v", time.Since(start))
	e.SetSocketWakes(false)
	e.SetNoticesWake(false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	var live []*core.Agent
	for _, l := range st.Agents {
		if l.Status == core.StatusActive && l.Token != "" {
			live = append(live, l)
		}
	}
	sort.Slice(live, func(i, j int) bool { return live[i].ID < live[j].ID })
	if len(live) == 0 {
		t.Fatal("no active agent with a token to measure check_in as")
	}
	tokens := make([]string, len(live))
	for i, l := range live {
		tokens[i] = l.Token
	}
	t.Logf("measuring as %d active agents", len(tokens))

	type step struct {
		name string
		fn   func(tok string) error
	}
	steps := []step{
		{"board", func(string) error { _, err := e.Board(ctx); return err }},
		{"inbox", func(tok string) error { _, err := e.Inbox(ctx, tok); return err }},
		{"check_in", func(tok string) error {
			_, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok, MailboxPage: true})
			return err
		}},
		{"sweep", func(string) error {
			_, err := e.query(ctx, func() core.Result { e.sweep(time.Now()); return core.Result{} })
			return err
		}},
		// What an idle daemon does every second, whatever else is happening.
		{"tick", func(string) error {
			_, err := e.query(ctx, func() core.Result {
				now := time.Now()
				e.sweep(now)
				e.expireWaiters(now)
				e.pruneSocketState()
				return core.Result{}
			})
			return err
		}},
	}
	if only := os.Getenv("DIBS_PROD_ONLY"); only != "" {
		var kept []step
		for _, s := range steps {
			if strings.Contains(","+only+",", ","+s.name+",") {
				kept = append(kept, s)
			}
		}
		steps = kept
	}
	if out := os.Getenv("DIBS_PROD_PROFILE"); out != "" {
		f, err := os.Create(out)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		if err := pprof.StartCPUProfile(f); err != nil {
			t.Fatal(err)
		}
		defer pprof.StopCPUProfile()
	}
	if b, err := e.Board(ctx); err == nil {
		raw, _ := json.Marshal(b)
		t.Logf("board reply: %d bytes", len(raw))
	}
	if res, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tokens[0], MailboxPage: true}); err == nil {
		raw, _ := json.Marshal(res)
		board, _ := json.Marshal(res["board"])
		t.Logf("check_in reply: %d bytes, of which board %d", len(raw), len(board))
	}
	rounds := 5
	if n, err := strconv.Atoi(os.Getenv("DIBS_PROD_ROUNDS")); err == nil && n > 0 {
		rounds = n
	}
	for _, s := range steps {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		alloc0 := ms.TotalAlloc
		start := time.Now()
		calls := 0
		for r := 0; r < rounds; r++ {
			for _, tok := range tokens {
				if err := s.fn(tok); err != nil && !strings.Contains(err.Error(), "E_") {
					t.Fatalf("%s: %v", s.name, err)
				}
				calls++
				if s.name == "board" || s.name == "sweep" || s.name == "tick" {
					break
				}
			}
		}
		runtime.ReadMemStats(&ms)
		took := time.Since(start)
		t.Logf("%-9s %5d calls  %9v/call  %8.1f KB alloc/call", s.name, calls,
			(took / time.Duration(calls)).Round(time.Microsecond),
			float64(ms.TotalAlloc-alloc0)/float64(calls)/1024)
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from) // #nosec G304 -- test input named by the operator
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
