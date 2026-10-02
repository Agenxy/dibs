package engine

import (
	"context"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Enter through real writer operations, then fold the actual ledger into a
// fresh engine. A copied live index would conceal a missing replay rebuild.
func TestInviteIssuerClosureSurvivesReplayAndReopening(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	led := &memLedger{}
	e := New(core.NewState("t", core.DefaultLimits()), led, deadProber{})
	e.SetRingCap(1)
	go e.Run(ctx)
	do := func(op *core.Op) core.Result {
		t.Helper()
		r, err := e.Do(ctx, op)
		if err != nil {
			t.Fatal("setup:", err)
		}
		return r
	}
	reg := do(&core.Op{Kind: core.OpRegister, Name: "issuer", Nonce: "retained-issuer", AgentKind: core.KindPersistent})
	token := reg["token"].(string)
	issuer, err := e.InvitationIssuer(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	do(&core.Op{Kind: core.OpAckBoard, Token: token})
	do(&core.Op{Kind: core.OpSpaceOpen, Token: token, Space: "work", Text: "work"})
	do(&core.Op{Kind: core.OpSpaceClose, Token: token, Space: "work"})
	assertLive := func(eng *Engine, want bool) {
		t.Helper()
		live, err := eng.InvitationIssuerCurrent(ctx, issuer.ID, issuer.Created, issuer.Closed)
		if err != nil || live != want {
			t.Fatalf("live = %v, want %v: %v", live, want, err)
		}
	}
	assertLive(e, true) // Closing a space must not close its director's children.
	do(&core.Op{Kind: core.OpSignOff, Token: token})
	reopened := do(&core.Op{Kind: core.OpRegister, Name: "issuer", Nonce: "retained-issuer", AgentKind: core.KindPersistent})
	current, err := e.InvitationIssuer(ctx, reopened["token"].(string))
	if err != nil || current.Closed == issuer.Closed {
		t.Fatalf("closure not recorded: %+v %v", current, err)
	}
	assertLive(e, false)
	newToken := reopened["token"].(string)
	do(&core.Op{Kind: core.OpAckBoard, Token: newToken})
	for _, text := range []string{"first", "second", "third", "fourth"} {
		do(&core.Op{Kind: core.OpSetSlot, Token: newToken, SlotID: "s1", Text: text})
	}
	var ops []*core.Op
	if _, err := e.query(ctx, func() core.Result { ops = append(ops, led.ops...); return core.Result{} }); err != nil {
		t.Fatal(err)
	}
	st := core.NewState("t", core.DefaultLimits())
	var history []core.Event
	for i, op := range ops {
		_, evs, err := st.Apply(op, time.Unix(1700000000+int64(i), 0))
		if err != nil {
			t.Fatalf("replay %d: %v", i, err)
		}
		history = append(history, evs...)
	}
	restart := New(st, &memLedger{}, deadProber{}, history)
	restart.SetRingCap(1)
	go restart.Run(ctx)
	assertLive(restart, false)
	live, err := restart.InvitationIssuerCurrent(ctx, current.ID, current.Created, current.Closed)
	if err != nil || !live {
		t.Fatalf("new generation refused after replay: %v %v", live, err)
	}
}

func TestInviteIssuerArchiveIsNotClosureAndReusedAddressIsNotItsIssuer(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	old := time.Unix(1700000000, 0)
	_, _, err := st.Apply(&core.Op{
		Kind: core.OpRegister, Name: "issuer", Nonce: "issuer-one",
		NewToken: "original", AgentKind: core.KindPersistent,
	}, old)
	if err != nil {
		t.Fatal(err)
	}
	a := st.Agents["issuer"]
	created := a.CreatedSerial
	// These are the inputs this derived predicate observes, not a claim about
	// which sweep decides to archive a row (covered by core's sweep tests).
	a.Status = core.StatusArchived
	e := New(st, &memLedger{}, deadProber{}, []core.Event{{Type: "agent.archived", Agent: a.ID, Serial: 2}})
	if !e.inviteIssuerCurrent(a.ID, created, 0) {
		t.Fatal("archival revoked children")
	}
	a.CreatedSerial = created + 10
	e.rebuildInviteClosures([]core.Event{
		{Type: "agent.closed", Agent: a.ID, Serial: 3},
		{Type: "agent.purged", Agent: a.ID, Serial: 4},
	})
	if e.inviteIssuerCurrent(a.ID, created, 0) {
		t.Fatal("old invitation inherited a reused ID")
	}
	if !e.inviteIssuerCurrent(a.ID, a.CreatedSerial, 0) {
		t.Fatal("old closure contaminated replacement issuer")
	}
}
