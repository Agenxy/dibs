package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// R19-2: a role a person set stands against a plain regrant for the rest of
// the run, and the decision is made on the loop with the grant itself.
func TestAHumanRoleChangeStandsAgainstARegrant(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "lead", AgentKind: core.KindPersistent, Nonce: "n-lead-0123456789abcdef"}); err != nil {
		t.Fatal("setup:", err)
	}
	if _, err := e.GrantRole(ctx, "lead", core.RoleAdmin); err != nil {
		t.Fatal("setup:", err)
	}
	if _, err := e.GrantRoleByHuman(ctx, "lead", core.RoleMember); err != nil {
		t.Fatal(err)
	}
	res, err := e.GrantRole(ctx, "lead", core.RoleAdmin) // the reconciler's tick
	if err != nil {
		t.Fatal(err)
	}
	if res["stands"] == nil || st.Agents["lead"].Role == core.RoleAdmin {
		t.Fatalf("a regrant after a person's demotion went through (%v, role %q): the predecessor "+
			"keeps admin through the handover", res, st.Agents["lead"].Role)
	}
}
