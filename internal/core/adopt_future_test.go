package core

import (
	"testing"
	"time"
)

// Adoption recovers the mail that exists. It does not capture the address.
//
// The result note said "only where its mail is delivered has changed", and an
// agent that had just adopted three mailboxes read that as a standing redirect:
// it told the operator it was now the delivery address for that name and would
// hand the address back if a Codex agent returned under it. That model is
// alarming and it is also wrong, and the wording is what produced it.
//
// What the operation does is re-address the messages that exist at that
// instant, once. Anything sent afterwards reaches whoever it is addressed to,
// including the original the moment it comes back. The difference is the whole
// safety of the operation: a standing redirect would be a coordinator-approvable
// interception of a live agent's mail, and this is a one-time recovery of mail
// nobody could read.
func TestAdoptionDoesNotRedirectFutureMail(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Now()
	sender := reg(t, s, "sender", "tok-send", now)
	lost := regPersistent(t, s, "codex-root", "tok-lost", "n-lost", now)
	heir := reg(t, s, "heir", "tok-heir", now)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: heir.Token}, now)

	// One message that exists BEFORE the adoption.
	mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: sender.Token, To: lost.ID,
		MsgType: MsgNotify, Body: "before",
	}, now)
	s.Agents[lost.ID].Status = StatusDormant

	res := mustApply(t, s, &Op{
		Kind: OpAdoptAgent, Token: heir.Token, To: lost.ID, AdoptAuthorised: true,
	}, now)
	t.Logf("adopt moved %v message(s)", res["messages"])

	// Now the original comes back and somebody writes to it again.
	s.Agents[lost.ID].Status = StatusActive
	mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: sender.Token, To: lost.ID,
		MsgType: MsgNotify, Body: "after",
	}, now)

	var toLost, toHeir int
	for _, m := range s.Messages {
		if m.Body != "after" {
			continue
		}
		switch m.To {
		case lost.ID:
			toLost++
		case heir.ID:
			toHeir++
		}
	}
	t.Logf("mail sent AFTER the adoption: to the original=%d, to the adopter=%d", toLost, toHeir)
	if toHeir > 0 {
		t.Errorf("mail sent to %q after it returned was delivered to %q instead. An "+
			"adoption would then be a standing interception of a live agent's mail",
			lost.ID, heir.ID)
	}
	if toLost != 1 {
		t.Errorf("mail sent to %q after it returned did not reach it (%d)", lost.ID, toLost)
	}
}
