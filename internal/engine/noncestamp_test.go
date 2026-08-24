package engine

import (
	"context"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// The engine must STAMP the nonce-restoration decision on every register.
//
// The fold is gated on Op.RestoreNonce so that a v0.0.6 ledger keeps replaying
// the way v0.0.6 replayed it. That gate only works if something actually sets
// the field on new ops, and the something is one line at ingress.
//
// A gate with nothing setting it is worse than no gate: the recovery quietly
// never happens, every core test still passes because those construct the Op
// by hand, and an archived admin comes back permanently without the role
// dibs.toml grants it. This repository has a name for that shape and a test
// enforcing it elsewhere: a parameter you declare but never read is invisible
// from outside, the call succeeds, and the effect silently does not happen.
//
// So this drives the whole path instead of the fold alone: register through the
// engine, archive the row, register again through the engine, and require the
// durable credential back. Delete the ingress line and this fails; the core
// tests would not.
func TestTheEngineStampsTheNonceRestorationDecision(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	const nonce = "the-durable-secret"
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "fleet-lead",
		AgentKind: core.KindPersistent, Nonce: nonce,
	}); err != nil {
		t.Fatal("setup register:", err)
	}

	// What archival does to the row: it survives, its live credential does not,
	// and the nonce INDEX is kept so recovery can still find it.
	l := st.Agents["fleet-lead"]
	if l == nil {
		t.Fatal("setup: no agent on the board after registering")
	}
	l.Status, l.Token, l.Nonce = core.StatusArchived, "", ""
	if st.Nonces[nonce] != "fleet-lead" {
		t.Fatal("setup: the nonce index is gone, so recovery cannot find this " +
			"row and the assertion below would be about a different path")
	}

	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "fleet-lead",
		AgentKind: core.KindPersistent, Nonce: nonce,
	}); err != nil {
		t.Fatal("recovering:", err)
	}

	if got := st.Agents["fleet-lead"].Nonce; got != nonce {
		t.Errorf("after recovery through the ENGINE the nonce is %q, want %q. The "+
			"fold is gated on Op.RestoreNonce and ingress is what sets it, so an "+
			"unstamped register leaves the agent with no durable identity: "+
			"AgentIdentity returns \"\" and a declared role can never reconcile "+
			"onto it again", got, nonce)
	}
}

// And the stamp must not leak onto ops that are not registers, because the
// field is on-disk format and a spurious one is a lie about what was decided.
func TestTheNonceStampIsOnlyOnRegisters(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	res, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "worker",
		AgentKind: core.KindPersistent, Nonce: "n-1",
	})
	if err != nil {
		t.Fatal("setup:", err)
	}
	tok, _ := res["token"].(string)
	if tok == "" {
		t.Fatal("setup: no token returned")
	}

	op := &core.Op{Kind: core.OpAckBoard, Token: tok}
	if _, err := e.Do(ctx, op); err != nil {
		t.Fatal("check_in:", err)
	}
	if op.RestoreNonce {
		t.Error("a non-register op was stamped with RestoreNonce. The field is " +
			"part of the on-disk format and records a decision about ONE branch " +
			"of register; setting it anywhere else writes a claim into the " +
			"ledger that no code made")
	}
	_ = time.Now
}
