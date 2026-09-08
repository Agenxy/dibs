package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// R10-1: the [roles] table resolves ids as well as names, so a row renamed
// for display is guarded by the id the table names.
func TestADeclaredIdIsGuardedWhateverTheRowIsNamed(t *testing.T) {
	const sid = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	e.SetPrivilegedNames([]string{"fleet-lead"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "fleet-lead", NewToken: "tok-1", AgentKind: core.KindPersistent, SessionID: sid})
	if err != nil || res["agent_id"] != "fleet-lead" {
		t.Fatal("setup:", res, err)
	}
	tok, _ := res["token"].(string) // the engine mints the token; the op's is not it
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpUpdate, Token: tok, Name: "Fleet Lead", Description: "d"}); err != nil {
		t.Fatal("setup: rename:", err)
	}
	if st.Agents["fleet-lead"].Name != "Fleet Lead" {
		t.Fatal("setup: the rename did not land, so the display name is not what is being recovered by")
	}
	_, err = e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "Fleet Lead", NewToken: "tok-2", AgentKind: core.KindPersistent, SessionID: sid})
	var ce *core.Error
	if err == nil || !errors.As(err, &ce) || ce.Code != "E_NEEDS_NONCE" {
		t.Fatalf("a row whose ID the [roles] table names was recovered by its display name and a "+
			"session id (%v): the guard read the name only, and the reconciler grants by id", err)
	}
}
