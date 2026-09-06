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

// The adoption twin of the pending-grant capture. An approved adoption moves
// the source mailbox into the requester's row, so a session-only reattach
// that takes that row's token before the human approves captures the whole
// mailbox. The guard covers a pending adoption the same way it covers a
// pending grant.
func TestARowWithAPendingAdoptionCannotBeRecoveredWithoutItsNonce(t *testing.T) {
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
	// The source whose mailbox the requester wants, made dormant.
	src, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "source", NewToken: "s-1", AgentKind: core.KindPersistent, Nonce: "n-src"})
	if err != nil {
		t.Fatal("setup:", err)
	}
	srcTok, _ := src["token"].(string)
	srcID, _ := src["agent_id"].(string)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpSignOff, Token: srcTok}); err != nil {
		t.Fatal("setup:", err)
	}
	// The requester: a default registration, reachable by name + session id.
	reg, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "asker", NewToken: "tok-1", SessionID: sid})
	if err != nil {
		t.Fatal("setup:", err)
	}
	askerTok, _ := reg["token"].(string)
	minted, _ := reg["nonce"].(string)
	// It asks the human to move the source mailbox onto it; the request sits pending.
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: askerTok, To: humanID,
		MsgType: core.MsgRequest, Body: "that mailbox is mine", Adopt: srcID,
	}); err != nil {
		t.Fatal("setup:", err)
	}

	res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "asker", NewToken: "tok-2", SessionID: sid})
	var ce *core.Error
	if err == nil || !errors.As(err, &ce) || ce.Code != "E_NEEDS_NONCE" {
		t.Fatalf("a session-only reattach of a row with a pending adoption got %v %v: "+
			"the human's approval would move the whole mailbox onto the taker", res, err)
	}
	// The requester itself still recovers, with its own minted nonce.
	if res, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "asker", NewToken: "tok-3", Nonce: minted, SessionID: sid,
	}); err != nil || res["agent_id"] != "asker" {
		t.Errorf("the requester could not recover with its own nonce during the pending adoption: %v %v", res, err)
	}
}

// The operator's own row is recovered only by opening the board, never by
// name and session id. A v0.0.6 archive-and-recovery blanks the human row's
// nonce while keeping the nonce index, so the blanked row became reachable
// by (name, session_id) like any nonce-less agent; both are public (the
// session id is the known human nonce), so this handed out the human's token
// and approval of the caller's own grant without Touch ID.
func TestTheHumanRowCannotBeRecoveredByNameAndSession(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	humanID, _, err := e.HumanAgent(ctx)
	if err != nil {
		t.Fatal("setup:", err)
	}
	// The historical state: the archive blanked Agent.Nonce, the nonce index
	// still points at the row, and it has since been recovered to active.
	st.Agents[humanID].Nonce = ""
	if st.Nonces[humanNonce()] != humanID {
		t.Fatalf("setup: the nonce index does not resolve the human row: %q", st.Nonces[humanNonce()])
	}

	// The public name and the public session id (which is the known nonce),
	// no nonce presented.
	res, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: humanName(), NewToken: "stolen",
		SessionID: humanNonce(),
	})
	var ce *core.Error
	if err == nil || !errors.As(err, &ce) || ce.Code != "E_NEEDS_NONCE" {
		t.Fatalf("a name-and-session register landed on the human row: got %v %v: the caller "+
			"holds the operator's token and can approve its own grant", res, err)
	}
}
