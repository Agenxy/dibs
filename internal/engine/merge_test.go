package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// MERGING IS ADMIN'S, and for a stronger reason than configure's: it moves one
// agent's mail into another's mailbox, getting the pair backwards sends a
// seat's history somewhere it does not belong, and the fold is ledgered so
// there is no undo.
func TestOnlyAnAdminMergesASeat(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	reg := func(name, nonce string) string {
		r, err := e.Do(ctx, &core.Op{
			Kind: core.OpRegister, Name: name, AgentKind: core.KindPersistent, Nonce: nonce,
		})
		if err != nil {
			t.Fatal("setup:", err)
		}
		tok, _ := r["token"].(string)
		return tok
	}
	admin, worker := reg("boss", "nb"), reg("hand", "nh")
	reg("seat", "n1")
	reg("seat-2", "n2")
	onLoop(t, ctx, e, func(st *core.State) {
		st.Agents["boss"].Role = core.RoleAdmin
		st.Agents["seat-2"].Status = core.StatusDormant
	})

	if _, err := e.MergeAgents(ctx, worker, "seat-2", "seat"); err == nil {
		t.Error("a non-admin merged two seats")
	}
	var merged bool
	onLoop(t, ctx, e, func(st *core.State) {
		merged = st.Agents["seat-2"].Status == core.StatusClosed
	})
	if merged {
		t.Fatal("and the merge happened anyway, which is worse than the error " +
			"being missing")
	}

	if _, err := e.MergeAgents(ctx, admin, "seat-2", "seat"); err != nil {
		t.Fatalf("the admin was refused: %v", err)
	}
	onLoop(t, ctx, e, func(st *core.State) {
		merged = st.Agents["seat-2"].Status == core.StatusClosed &&
			st.Agents["seat-2"].MergedInto == "seat"
	})
	if !merged {
		t.Error("the admin's merge reported success and did not happen")
	}
}
