package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// The holder of a pinned credential is found through the nonce index as
// well as the row.
//
// A sweep written before v0.0.8 blanked an archived row's nonce and kept the
// index entry that lets it resume, so on a replayed board the credential
// lives in state.Nonces alone. Role withdrawal looks the holder up by that
// credential; a search of the rows found nobody, the pin was dropped, and the
// archived holder could resume into the role the config had withdrawn. Round
// six of the pre-release review.
func TestAgentByIdentityReadsTheNonceIndexTheOldSweepLeft(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "lead", Nonce: "nonce-lead"})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res["agent_id"].(string)
	// What the old sweep left behind: archived, row nonce blanked, index kept.
	_, _ = e.query(ctx, func() core.Result {
		e.state.Agents[id].Status = core.StatusArchived
		e.state.Agents[id].Nonce = ""
		return core.Result{}
	})
	if st.Nonces["nonce-lead"] != id {
		t.Fatal("setup: the nonce index does not hold the credential")
	}

	got, err := e.AgentByIdentity(ctx, RolePinFingerprint("nonce-lead"))
	if err != nil {
		t.Fatal(err)
	}
	if got != id {
		t.Errorf("AgentByIdentity = %q, want %q: the credential the index still honours for "+
			"resume was invisible to the withdrawal that should revoke its role", got, id)
	}
}
