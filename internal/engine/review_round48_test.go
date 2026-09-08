package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A register minting a sibling with an active agent's token takes that
// agent's thread alias; if the same register states a primary session held
// by a dormant row, the ingress recorded only the dormant row as the one the
// take came from, so the alias stayed on the active row too: two active
// holders of one thread, hooks resolving to either. The caller's token names
// its row as surely as the ingress does.
func TestASiblingTakingTwoBindingsLeavesNeitherHolderBehind(t *testing.T) {
	const (
		aliasT   = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
		primaryS = "01a00042-2222-7f60-81cc-6ab1298d76ec"
	)
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	a, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "a", Nonce: "n-a-0123456789abcdef", AgentKind: core.KindPersistent, SessionAlias: aliasT, Agent: &core.AgentInfo{Harness: "Codex"}})
	if err != nil {
		t.Fatal("setup:", err)
	}
	tokA, _ := a["token"].(string)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "b", Nonce: "n-b-0123456789abcdef", AgentKind: core.KindPersistent, SessionID: primaryS, Agent: &core.AgentInfo{Harness: "Codex"}}); err != nil {
		t.Fatal("setup:", err)
	}
	st.Agents["b"].Status = core.StatusDormant
	if !st.Agents["a"].HoldsSessionForTest(aliasT) || !st.Agents["b"].HoldsSessionForTest(primaryS) {
		t.Fatal("setup: the two bindings are not where the scenario needs them")
	}
	// The sibling, minted with a's token, stating b's primary and a's alias.
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "sibling", Token: tokA, Nonce: "n-s-0123456789abcdef", AgentKind: core.KindPersistent, SessionID: primaryS, SessionAlias: aliasT, Agent: &core.AgentInfo{Harness: "Codex"}}); err != nil {
		t.Fatal("setup:", err)
	}
	sib := st.Agents["sibling"]
	if sib == nil || !sib.HoldsSessionForTest(aliasT) || !sib.HoldsSessionForTest(primaryS) {
		t.Fatal("setup: the sibling did not take both bindings, so nothing below is contested")
	}
	if st.Agents["a"].HoldsSessionForTest(aliasT) {
		t.Fatal("the active row whose token minted the sibling still holds the thread the sibling took: " +
			"two active holders, and every hook a coin flip")
	}
	if st.Agents["b"].HoldsSessionForTest(primaryS) {
		t.Fatal("the dormant row still holds the primary the sibling took")
	}
}
