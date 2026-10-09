// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/ledger"
)

func TestDeclarationAgeRebuildsFromRealReplayAndReadsNeverWrite(t *testing.T) {
	dir := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ledger")
	led, err := ledger.Open(path, "age", box)
	if err != nil {
		t.Fatal(err)
	}
	state := core.NewState("age", core.DefaultLimits())
	var history []core.Event
	led.OnEvents = func(events []core.Event) { history = append(history, events...) }
	// These are accepted historical records, committed to the real encrypted
	// ledger. Their event times are the clock input; no age flag is set by hand.
	at := time.Now().Round(0).Add(-2 * time.Hour)
	e := New(state, led, nil)
	for _, op := range []*core.Op{
		{Kind: core.OpRegister, Name: "worker", NewToken: "test-token", Nonce: "age-worker", AgentKind: core.KindPersistent},
		{Kind: core.OpAckBoard, Token: "test-token"},
		{Kind: core.OpSetSlot, Token: "test-token", SlotID: "s1", Text: "unchanged work"},
	} {
		if _, err := e.applyAndLedger(op, at); err != nil {
			t.Fatal("setup historical append:", err)
		}
	}
	if err := led.Close(); err != nil {
		t.Fatal(err)
	}
	led, err = ledger.Open(path, "age", box)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = led.Close() }()
	state = core.NewState("age", core.DefaultLimits())
	led.OnEvents = func(events []core.Event) { history = append(history, events...) }
	if _, err := led.Replay(state); err != nil {
		t.Fatal("setup replay:", err)
	}
	restored := New(state, led, nil, history)
	serial := state.Serial
	// Draw before any boot grace, as a read-only embedding does. The writer
	// query does not synthesize contact or alter the unchanged declaration.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ctx.Done():
				close(done)
				return
			case request := <-restored.ops:
				restored.serveRequest(request)
			}
		}
	}()
	defer func() { cancel(); <-done }()
	for range 2 {
		board, err := restored.Board(ctx)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(board)
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			Agents []struct {
				ID    string `json:"id"`
				Work  string `json:"work"`
				Slots []struct {
					Age int64 `json:"unchanged_for_s"`
				} `json:"slots"`
			} `json:"agents"`
		}
		if err := json.Unmarshal(encoded, &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Agents) != 1 || len(payload.Agents[0].Slots) != 1 {
			t.Fatalf("setup board: %s", encoded)
		}
		row := payload.Agents[0]
		if row.Work != "stalled" || row.Slots[0].Age < 7200 {
			t.Errorf("wall age missing after replay: %s", encoded)
		}
		if state.Serial != serial {
			t.Fatal("age read changed replayable state")
		}
	}
}
