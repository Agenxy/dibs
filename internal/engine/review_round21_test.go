package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// R21-1: a register that carries the holder's token but mints a new row
// moves the thread to that row; it does not leave two live holders. Once
// for the stated id and once for the alias, because each is vetted by its
// own guard and the other guard's fix would cover a test that carried both.
func TestASiblingMintedWithTheHoldersTokenTakesTheThread(t *testing.T) {
	const thread = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	for name, carry := range map[string]func(op *core.Op){
		"by session_id": func(op *core.Op) { op.SessionID = thread },
		"by alias":      func(op *core.Op) { op.SessionAlias = thread },
	} {
		t.Run(name, func(t *testing.T) {
			st := core.NewState("t", core.DefaultLimits())
			e := New(st, &memLedger{}, deadProber{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go e.Run(ctx)
			first, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "first", AgentKind: core.KindPersistent, Nonce: "n-first-0123456789abcdef", SessionID: thread, SessionAlias: thread})
			if err != nil {
				t.Fatal("setup:", err)
			}
			tok, _ := first["token"].(string)
			sibling := &core.Op{Kind: core.OpRegister, Name: "sibling", AgentKind: core.KindPersistent, Nonce: "n-sibling-0123456789abcdef", Token: tok}
			carry(sibling)
			if _, err := e.Do(ctx, sibling); err != nil {
				t.Fatal(err)
			}
			a, b := st.Agents["first"].HoldsSessionForTest(thread), st.Agents["sibling"].HoldsSessionForTest(thread)
			if a && b {
				t.Fatal("two live rows hold the same thread after a register carrying the holder's token: " +
					"hook resolution is a coin flip and two mailboxes wake one session")
			}
			if !b {
				t.Error("the row the caller minted does not hold the thread it stated")
			}
		})
	}
}

// R21-2: an ambient repair binds only a row that has no session; one that
// was bound by a check_in in the meantime keeps what it bound.
func TestAnAmbientRepairDoesNotOverwriteABindingMadeMeanwhile(t *testing.T) {
	const (
		ambient = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
		real    = "01a00042-2222-7f60-81cc-6ab1298d76ec"
	)
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "w", AgentKind: core.KindPersistent, Nonce: "n-w-0123456789abcdef"})
	if err != nil {
		t.Fatal("setup:", err)
	}
	tok, _ := res["token"].(string)
	// The check_in that lands between the repair's question and its bind.
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok, SessionAlias: real}); err != nil {
		t.Fatal("setup:", err)
	}
	adopted, err := e.AdoptSession(ctx, tok, ambient)
	if err != nil {
		t.Fatal(err)
	}
	l := st.Agents["w"]
	if adopted || l.HoldsSessionForTest(ambient) || !l.HoldsSessionForTest(real) {
		t.Fatalf("the ambient repair reported %v and the row holds ambient=%v real=%v: a binding "+
			"the agent made was overwritten and ledgered", adopted, l.HoldsSessionForTest(ambient), l.HoldsSessionForTest(real))
	}
}
