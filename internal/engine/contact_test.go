package engine

import (
	"context"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestAuthenticatedContactExpiresAndCrashDetectionReturns(t *testing.T) {
	st := core.NewState("contact", core.DefaultLimits())
	e := New(st, &memLedger{}, onePIDDead{dead: 424242})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	r, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "worker", PID: 424242,
		Agent: &core.AgentInfo{Harness: "codex"},
	})
	if err != nil {
		t.Fatal("register setup:", err)
	}
	token := r["token"].(string)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: token}); err != nil {
		t.Fatal("contact setup:", err)
	}
	_, err = e.query(ctx, func() core.Result {
		now := time.Now()
		e.sweep(now)
		if st.Agents["worker"].Status != core.StatusActive {
			t.Error("actual check_in did not protect a fresh identity")
		}
		e.sweep(now.Add(st.Limits.IdleTTL + time.Second))
		if st.Agents["worker"].StaleReason != "process_exited" {
			t.Error("expired contact hid a crashed process")
		}
		if len(e.contact) != 0 {
			t.Error("expired evidence remained cached")
		}
		return core.Result{}
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBootGraceAndBackgroundObservationDoNotOverrideADeadPID(t *testing.T) {
	st := core.NewState("contact", core.DefaultLimits())
	if _, _, err := st.Apply(&core.Op{Kind: core.OpRegister, Name: "worker", NewToken: "token", PID: 424242}, time.Now()); err != nil {
		t.Fatal("ledger fixture setup:", err)
	}
	e := New(st, &memLedger{}, onePIDDead{dead: 424242})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	id, _, err := e.SubscribeInfo(ctx, "token") // production background-auth door
	if err != nil || id != "worker" {
		t.Fatal("subscription setup failed", err)
	}
	_, err = e.query(ctx, func() core.Result {
		if e.seen["worker"].IsZero() {
			t.Error("setup: boot did not grant freshness grace")
		}
		e.sweep(time.Now())
		if st.Agents["worker"].StaleReason != "process_exited" {
			t.Error("boot/subscription grace fabricated model contact")
		}
		return core.Result{}
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Decision inputs are synthetic here; the separate MCP regression verifies
// that production authentication actually populates this cache.
func TestContactEvidenceCannotCrossAnAgentIncarnation(t *testing.T) {
	st := core.NewState("contact", core.DefaultLimits())
	e := &Engine{state: st, contact: map[string]contactEvidence{
		"worker": {at: time.Now(), created: 7},
	}}
	successor := &core.Agent{ID: "worker", CreatedSerial: 8, Status: core.StatusActive}
	st.Agents["worker"] = successor
	if e.recentAuthenticatedContact(successor, time.Now()) {
		t.Fatal("successor inherited predecessor contact")
	}
	e.trimContactEvidence(time.Now())
	if len(e.contact) != 0 {
		t.Fatal("replaced incarnation retained cached evidence")
	}
}
