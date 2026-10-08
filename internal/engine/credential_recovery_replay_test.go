// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

func TestCredentialRecoveryWritesOnlySelectedNonceAndReplays(t *testing.T) {
	var logs continuationWakeLog
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	st := core.NewState("retention", core.DefaultLimits())
	led := &retentionLedger{}
	e := New(st, led, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	register := func(nonce string) core.Result {
		t.Helper()
		r, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "worker", Nonce: nonce, Agent: &core.AgentInfo{HostID: "retention"}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	old := register("old-secret")
	_ = register("new-secret")
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpSweep, StaleAgents: []string{old["agent_id"].(string)}}); err != nil {
		t.Fatal(err)
	}
	r, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "worker", RecoveryNonces: []string{"new-secret", "old-secret"}, Agent: &core.AgentInfo{HostID: "retention"}})
	if err != nil || r["agent_id"] != old["agent_id"] {
		t.Fatalf("credential selection failed: %v / %v", r, err)
	}
	_, err = e.query(ctx, func() core.Result {
		raw := led.records[len(led.records)-1].op
		var recorded core.Op
		if err := json.Unmarshal(raw, &recorded); err != nil {
			t.Error(err)
		}
		if recorded.Kind != core.OpRegister || recorded.Nonce != "old-secret" ||
			strings.Contains(string(raw), "new-secret") || strings.Contains(string(raw), "recovery_nonces") {
			t.Error("append did not receive only the selected credential in the ordinary register op")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	(&retentionBoard{t: t, e: e, ctx: ctx, led: led}).assertReplay()
	if !strings.Contains(logs.String(), "retained credentials resolved to oldest identity") {
		t.Fatal("setup: recovery diagnostic was not captured")
	}
	for _, secret := range []string{"new-secret", "old-secret"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatal("recovery diagnostic exposed a credential")
		}
	}
}

func TestCredentialRecoveryCannotBecomeTheHuman(t *testing.T) {
	st := core.NewState("human-recovery", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	id, humanToken, err := e.HumanAgent(ctx)
	if err != nil || id == "" {
		t.Fatalf("human setup: %v", err)
	}
	// Correct the host through the authenticated update path, as an operator
	// may do; matching host evidence still cannot authorize its recovery.
	_, err = e.Do(ctx, &core.Op{
		Kind: core.OpUpdate, Token: humanToken,
		Description: "the human at the board", Agent: &core.AgentInfo{HostID: "human-recovery"},
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: humanName(), RecoveryNonces: []string{humanNonce(), "unknown"}, Agent: &core.AgentInfo{HostID: "human-recovery"}})
	if err == nil || r != nil {
		t.Fatal("candidate selection bypassed the ordinary human-identity guard")
	}
}
