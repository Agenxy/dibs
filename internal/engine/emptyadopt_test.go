package engine

import (
	"errors"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// An adoption that would move nothing is refused, not ledgered.
//
// THE INVARIANT THIS DEFENDS. The fold records no adoption relationship:
// moving the messages IS the entire effect. So adopting an empty mailbox
// changed no replayable state, advanced the serial anyway, and answered
// ok:true with "read them with inbox", which is advice about mail that does not
// exist. "An op is ledgered iff it changed replayable state" is one of the four
// rules in AGENTS.md, and this broke it in the direction nobody notices,
// because success looks like success.
//
// Refused at INGRESS rather than fixed in the fold. Making Apply skip finish()
// would change which ops advance the serial, and any ledger already holding an
// empty adoption would replay with a different serial sequence than it was
// written with, dragging every msg_serial and cursor that references it along.
//
// Found by a pre-release review.
func TestAdoptingAnEmptyMailboxIsRefused(t *testing.T) {
	e, ctx, cancel := runningEngine(t)
	defer cancel()

	if err := e.refuseEmptyAdoption("ghost-agent"); err != nil {
		t.Fatalf("a source that does not exist is the fold's error to report, with "+
			"its own hint; this must not pre-empt it: %v", err)
	}

	reg := func(name, nonce string) (id, token string) {
		t.Helper()
		res, err := e.Do(ctx, &core.Op{
			Kind: core.OpRegister, Name: name, AgentKind: core.KindPersistent,
			Nonce: nonce, NewToken: "tok-" + name,
		})
		if err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
		id, _ = res["agent_id"].(string)
		token, _ = res["token"].(string)
		if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: token}); err != nil {
			t.Fatalf("ack %s: %v", name, err)
		}
		return id, token
	}
	quiet, quietTok := reg("quiet", "quiet-nonce")
	_, senderTok := reg("sender", "sender-nonce")

	// Empty: refused.
	err := e.refuseEmptyAdoption(quiet)
	if err == nil {
		t.Fatal("an adoption that moves nothing was allowed through: it advances the " +
			"serial, ledgers an op that changed no state, and tells the caller to go " +
			"and read mail that is not there")
	}
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Code != "E_NOTHING_TO_ADOPT" {
		t.Errorf("refused with %v, want E_NOTHING_TO_ADOPT", err)
	}
	if ce.Hint == "" {
		t.Error("no hint: an agent told only that it failed cannot tell whether it " +
			"named the wrong row or the row is simply empty")
	}

	// MAIL THE HEIR COULD NOT READ IS NOT MAIL TO RESCUE.
	//
	// The check counted any retained record, and a consumed terminal one is
	// retained for fifteen minutes after it is answered. So: notify an agent,
	// let it acknowledge, let it go dormant, adopt it. The check saw a record,
	// the fold moved and counted it, and the answer was ok:true with a positive
	// count and "read them with inbox" into an inbox that shows nothing, because
	// Inbox excludes exactly those. The rescue reported, the mailbox empty.
	// Found by a pre-release review; it is the second time this predicate has
	// been not quite the same one the recipient uses.
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: senderTok, To: quiet,
		MsgType: core.MsgNotify, Body: "read and done with",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	inbox := e.state.Inbox(quiet)
	if len(inbox) != 1 {
		t.Fatalf("setup: the quiet agent has %d messages, want 1", len(inbox))
	}
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpAckMessage, Token: quietTok, MsgSerial: inbox[0].Serial,
	}); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if got := len(e.state.Inbox(quiet)); got != 0 {
		t.Fatalf("setup: the message is still visible (%d), so this proves nothing", got)
	}
	if err := e.refuseEmptyAdoption(quiet); err == nil {
		t.Error("an adoption was allowed against a mailbox holding only mail its owner " +
			"had already read: the fold moves and counts those records, so the " +
			"coordinator is told a rescue happened and the heir's inbox is empty")
	}

	// One message the owner has NOT read, and it is allowed.
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: senderTok, To: quiet,
		MsgType: core.MsgNotify, Body: "something to rescue",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := e.refuseEmptyAdoption(quiet); err != nil {
		t.Errorf("a mailbox with mail in it was refused, which removes the feature "+
			"rather than the no-op: %v", err)
	}
}
