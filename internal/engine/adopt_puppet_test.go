package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A coordinator cannot become the reader of an abandoned mailbox by minting
// the "third party" it moves the mailbox onto.
//
// The self-adoption rule compared ids: onto itself is contents, onto anyone
// else is custody. So the two-call route was: register a second agent from
// the same session, keep its token (register hands the token to the caller),
// adopt the stranded mailbox `into` it, and read with that token. Approving
// that agent's request to adopt is the same route through the other door.
// Found by the pre-release review, round three.
//
// What the board can see is where a registration came from: the stdio bridge
// stamps every call with the session it runs inside, and the daemon records
// that on the row it mints (Agent.RegisteredFrom) before the session vetting
// clears it as an alias. An agent registered from a session the coordinator
// holds, or from the session the coordinator itself was registered from, is
// the coordinator under another name. A proven child is too.
func TestACoordinatorCannotAdoptOntoAnAgentItMintedInItsOwnSession(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	coordinator, member := censusBoard(t, ctx, e)

	const session = "0123abcd-4567-89ef-0123-456789abcdef"
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpBindSession, Token: coordinator, SessionID: session}); err != nil {
		t.Fatalf("setup: bind the coordinator's session: %v", err)
	}
	// The puppet: a fresh name, a session id the model typed so the thread
	// guard stands aside, and the bridge's stamp saying which session the
	// call really came from.
	res, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "puppet", SessionID: "made-up", RegisteredFrom: session,
	})
	if err != nil {
		t.Fatalf("setup: register the puppet: %v", err)
	}
	puppet, _ := res["token"].(string)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: puppet}); err != nil {
		t.Fatal("setup:", err)
	}

	var ce *core.Error
	_, err = e.Do(ctx, &core.Op{Kind: core.OpAdoptAgent, Token: coordinator, To: "stranded", Space: "puppet"})
	if !errors.As(err, &ce) || ce.Code != "E_NOT_PERMITTED" {
		t.Fatalf("adopting onto an agent minted in the coordinator's own session: %v, "+
			"want E_NOT_PERMITTED: the coordinator holds that agent's token", err)
	}

	// The other door: the puppet asks, the coordinator approves.
	sent, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: puppet, To: "coord",
		MsgType: core.MsgRequest, Body: "I will take stranded's mail", Adopt: "stranded",
	})
	if err != nil {
		t.Fatal("setup:", err)
	}
	_, err = e.Do(ctx, &core.Op{
		Kind: core.OpRespond, Token: coordinator, MsgSerial: sent["msg_serial"].(uint64),
		Disposition: "approve",
	})
	if !errors.As(err, &ce) || ce.Code != "E_NOT_PERMITTED" {
		t.Fatalf("approving the puppet's adoption request: %v, want E_NOT_PERMITTED", err)
	}

	// A genuine third party, registered from a session of its own, is still
	// custody: the consolidation the role exists for.
	_ = member
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAdoptAgent, Token: coordinator, To: "stranded", Space: "member"}); err != nil {
		t.Fatalf("adopting onto a genuine third party: %v, want it to succeed", err)
	}
}
