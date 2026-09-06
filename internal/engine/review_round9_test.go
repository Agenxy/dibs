package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// R9-1: a row that holds a role is recovered by its nonce only. Name plus
// session id, neither secret, used to hand out a fresh admin token.
func TestAPrivilegedRowCannotBeRecoveredWithoutItsNonce(t *testing.T) {
	const sid = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	first, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "fleet-lead", NewToken: "tok-1", AgentKind: core.KindPersistent, SessionID: sid})
	if err != nil {
		t.Fatal("setup:", err)
	}
	minted, _ := first["nonce"].(string)
	if minted == "" {
		t.Fatal("setup: no nonce was minted, so session-based recovery is not the path under test")
	}
	if _, err := e.GrantRole(ctx, "fleet-lead", "admin"); err != nil {
		t.Fatal("setup:", err)
	}
	// Anyone with the name and the session id, and no nonce.
	res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "fleet-lead", NewToken: "tok-2", AgentKind: core.KindPersistent, SessionID: sid})
	var ce *core.Error
	if err == nil || !errors.As(err, &ce) || ce.Code != "E_NEEDS_NONCE" {
		t.Fatalf("a register with the admin's name and session id and no nonce got %v %v: "+
			"an admin token for anyone who can read a session id off a hook", res, err)
	}
	// The row's own nonce still works.
	if res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "fleet-lead", NewToken: "tok-3", AgentKind: core.KindPersistent, Nonce: minted, SessionID: sid}); err != nil || res["agent_id"] != "fleet-lead" {
		t.Errorf("the nonce the row was given no longer recovers it: %v %v", res, err)
	}
}

// A name the operator declared for a role is guarded before its first grant,
// because the pinned fingerprint stays with the row and the reconciler would
// grant to whoever recovered it.
func TestADeclaredNameCannotBeRecoveredWithoutItsNonceBeforeItsGrant(t *testing.T) {
	const sid = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	e.SetPrivilegedNames([]string{"orchestrator"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "orchestrator", NewToken: "tok-1", AgentKind: core.KindPersistent, SessionID: sid}); err != nil {
		t.Fatal("setup:", err)
	}
	_, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "orchestrator", NewToken: "tok-2", AgentKind: core.KindPersistent, SessionID: sid})
	var ce *core.Error
	if err == nil || !errors.As(err, &ce) || ce.Code != "E_NEEDS_NONCE" {
		t.Fatalf("a declared name with no role yet was recovered by name and session id: %v", err)
	}
}

// R9-3, from the wake's side: bind_session(B) after A means the wake resumes B.
func TestAnExplicitRebindMovesTheWake(t *testing.T) {
	const (
		a = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
		b = "01a00042-2222-7f60-81cc-6ab1298d76ec"
	)
	st := core.NewState("test", core.DefaultLimits())
	for _, o := range []*core.Op{
		{Kind: core.OpRegister, Name: "r", NewToken: "tok", AgentKind: core.KindPersistent, Nonce: "n", Agent: &core.AgentInfo{Harness: "Codex"}, SessionAlias: a, V7Semantics: true},
		{Kind: core.OpBindSession, Token: "tok", SessionID: b, V7Semantics: true},
	} {
		if _, _, err := st.Apply(o, t0Engine()); err != nil {
			t.Fatal("setup:", err)
		}
	}
	if got := threadIDOf(st.Agents["r"]); got != b {
		t.Errorf("after bind_session(%s) the wake resumes %s", b, got)
	}
	if ids := sessionsOf(st.Agents["r"]); len(ids) == 0 || ids[0] != b {
		t.Errorf("the socket route tries %v first; the bound session is %s", ids, b)
	}
}
