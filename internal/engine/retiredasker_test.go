package engine

import (
	"errors"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// Approving a request from an agent that has retired must not perform it.
//
// THE LOSS THIS PREVENTS. Apply notices the requester is gone and marks the
// response undelivered, then performs the effects anyway: the role is granted
// to an agent that cannot act, and an adopted mailbox is moved INTO one whose
// token has been blanked and which cannot resume. A coordinator approving a
// rescue therefore moved the rescued mail somewhere nobody can ever read it,
// and was told it worked. The direct adoption path already refuses a closed or
// archived destination; only the approval route did not.
//
// Refused at ingress, because refusing inside the fold would bind every
// approval already on disk, and skipping the effect there would make replay
// produce a different board from the one that was written.
func TestApprovingARetiredAgentsRequestIsRefused(t *testing.T) {
	e, ctx, cancel := runningEngine(t)
	defer cancel()

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
	askerID, askerTok := reg("asker", "asker-nonce")
	coordID, coordTok := reg("coord", "coord-nonce")
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpGrantRole, To: coordID, Mode: core.RoleCoordinator,
	}); err != nil {
		t.Fatalf("grant: %v", err)
	}

	// A dormant agent with mail to rescue, so the request is a real one.
	oldID, oldTok := reg("old", "old-nonce")
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: askerTok, To: oldID,
		MsgType: core.MsgNotify, Body: "worth rescuing",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpSignOff, Token: oldTok}); err != nil {
		t.Fatalf("sign off the source: %v", err)
	}

	sent, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: askerTok, To: coordID,
		MsgType: core.MsgRequest, Body: "may I take its mailbox?", Adopt: oldID,
	})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	serial, _ := sent["msg_serial"].(uint64)

	// The asker retires while the request is outstanding.
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpSignOff, Token: askerTok}); err != nil {
		t.Fatalf("sign off the asker: %v", err)
	}
	if got := e.state.Agents[askerID].Status; got != core.StatusClosed {
		t.Fatalf("setup: the asker is %s, so this proves nothing", got)
	}

	_, err = e.Do(ctx, &core.Op{
		Kind: core.OpRespond, Token: coordTok, MsgSerial: serial, Disposition: "approve",
	})
	if err == nil {
		t.Fatal("approving moved a mailbox into an agent that has retired: its token " +
			"is blanked and it cannot resume, so the rescued mail is now somewhere " +
			"nobody can read, and the coordinator was told it worked")
	}
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Code != "E_AGENT_CLOSED" {
		t.Errorf("refused with %v, want E_AGENT_CLOSED", err)
	}
	if ce != nil && ce.Hint == "" {
		t.Error("no hint: the coordinator is left without the next move")
	}
}
