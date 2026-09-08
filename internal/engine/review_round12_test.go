package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// R12-1: a returning agent registers with its nonce and no token. Its own
// alias is its own, and a return to an earlier thread makes that thread the
// wake target; the ingress used to see a stranger and clear the alias.
func TestAReturningAgentMayReclaimItsOwnThreadByNonce(t *testing.T) {
	const (
		a = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
		b = "01a00042-2222-7f60-81cc-6ab1298d76ec"
	)
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "r", AgentKind: core.KindPersistent, Nonce: "n-r-0123456789abcdef", Agent: &core.AgentInfo{Harness: "Codex"}, SessionAlias: a})
	if err != nil {
		t.Fatal("setup:", err)
	}
	tok, _ := res["token"].(string)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok, SessionAlias: b}); err != nil {
		t.Fatal("setup:", err)
	}
	if st.Agents["r"].CurrentSession != b {
		t.Fatal("setup: b did not become current, so the return below proves nothing")
	}
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "r", AgentKind: core.KindPersistent, Nonce: "n-r-0123456789abcdef", Agent: &core.AgentInfo{Harness: "Codex"}, SessionAlias: a}); err != nil {
		t.Fatal(err)
	}
	if got := threadIDOf(st.Agents["r"]); got != a {
		t.Errorf("after returning to thread %s by nonce the wake resumes %s: the ingress refused "+
			"the agent its own alias because it carried no token", a, got)
	}
}
