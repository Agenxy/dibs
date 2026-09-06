package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A row one yes away from a role can be captured by a session-only reattach.
// A default registrant asks the human for coordinator; before the human
// decides, another caller re-registers with the requester's public name and
// session id and no nonce, taking the row's token; the human's approval then
// lands coordinator on the taker. The guard refuses a session-only reattach
// of a row with a pending grant request.
func TestARowWithAPendingGrantCannotBeRecoveredWithoutItsNonce(t *testing.T) {
	const sid = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	humanID, _, err := e.HumanAgent(ctx)
	if err != nil {
		t.Fatal("setup:", err)
	}
	// The requester: a default registration, reachable by name + session id.
	reg, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "asker", NewToken: "tok-1", SessionID: sid})
	if err != nil {
		t.Fatal("setup:", err)
	}
	askerTok, _ := reg["token"].(string)
	minted, _ := reg["nonce"].(string)
	if minted == "" {
		t.Fatal("setup: no nonce was minted, so the nonce recovery path is not under test")
	}
	// It asks the human for coordinator; the request sits pending.
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: askerTok, To: humanID,
		MsgType: core.MsgRequest, Body: "promote me", Grant: core.RoleCoordinator,
	}); err != nil {
		t.Fatal("setup:", err)
	}

	// The taker: the requester's name and session id, no nonce.
	res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "asker", NewToken: "tok-2", SessionID: sid})
	var ce *core.Error
	if err == nil || !errors.As(err, &ce) || ce.Code != "E_NEEDS_NONCE" {
		t.Fatalf("a session-only reattach of a row with a pending coordinator grant got %v %v: "+
			"the human's approval would promote the taker", res, err)
	}
	// The requester itself still recovers, with the nonce it was minted.
	if res, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "asker", NewToken: "tok-3", Nonce: minted, SessionID: sid,
	}); err != nil || res["agent_id"] != "asker" {
		t.Errorf("the requester could not recover with its own nonce during the pending grant: %v %v", res, err)
	}
}
