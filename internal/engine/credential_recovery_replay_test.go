package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestCredentialRecoveryWritesOnlySelectedNonceAndReplays(t *testing.T) {
	st := core.NewState("recovery-replay", core.DefaultLimits())
	led := &memLedger{}
	e := New(st, led, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	register := func(nonce string) core.Result {
		t.Helper()
		r, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "worker", Nonce: nonce, Agent: &core.AgentInfo{HostID: "recovery-replay"}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	old := register("old-secret")
	_ = register("new-secret")
	r, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "worker", RecoveryNonces: []string{"new-secret", "old-secret"}, Agent: &core.AgentInfo{HostID: "recovery-replay"}})
	if err != nil || r["agent_id"] != old["agent_id"] {
		t.Fatalf("credential selection failed: %v / %v", r, err)
	}
	_, err = e.query(ctx, func() core.Result {
		fold := core.NewState("recovery-replay", core.DefaultLimits())
		for _, op := range led.ops {
			if len(op.RecoveryNonces) != 0 {
				t.Error("candidate credentials reached append")
			}
			raw, jerr := json.Marshal(op)
			if jerr != nil {
				t.Error(jerr)
				continue
			}
			if strings.Contains(string(raw), "recovery_nonces") {
				t.Error("transient candidate list entered on-disk format")
			}
			if _, _, aerr := fold.Apply(op, time.Now()); aerr != nil {
				t.Error(aerr)
			}
		}
		if fold.Nonces["old-secret"] != st.Nonces["old-secret"] || len(fold.Agents) != len(st.Agents) || fold.Serial != st.Serial {
			t.Error("selected registration did not replay to the same identities/serial")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
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
