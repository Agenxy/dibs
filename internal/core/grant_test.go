package core

import "testing"

// Approving a role request IS the grant.
//
// It used to record that somebody agreed one should happen, after which the
// person went to a terminal and typed `dibs admin coordinator <agent>`. Two
// steps for one decision, and the second is where it died: the approval sat
// answered on the board while the agent stayed unable to do the thing it had
// just been told it could. Measured on this project's own board, where an agent
// needed coordinator to broadcast and the human had approved it.
func TestApprovingARoleRequestGrantsTheRole(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	reg(t, s, "asker", "ta", t0)
	reg(t, s, "human", "th", t0)

	res := mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "ta", To: "human", MsgType: MsgRequest,
		Body: "so I can broadcast the freeze", Grant: RoleCoordinator,
	}, t0)
	ser := res["msg_serial"].(uint64)

	if s.Agents["asker"].IsCoordinator() {
		t.Fatal("setup: the asker already held the role, so this proves nothing")
	}

	out := mustApply(t, s, &Op{
		Kind: OpRespond, Token: "th", MsgSerial: ser, Disposition: "approve",
	}, t0)

	if !s.Agents["asker"].IsCoordinator() {
		t.Error("the request was approved and the agent still cannot act. An approval " +
			"that only records agreement leaves the person with a second step nobody " +
			"told them about, on a board that says yes")
	}
	if out["granted"] != RoleCoordinator || out["to"] != "asker" {
		t.Errorf("the result does not say what was granted: %v", out)
	}
}

// Denying grants nothing, which is the other half and the easier one to lose.
func TestDenyingARoleRequestGrantsNothing(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	reg(t, s, "asker", "ta", t0)
	reg(t, s, "human", "th", t0)

	res := mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "ta", To: "human", MsgType: MsgRequest,
		Body: "let me coordinate", Grant: RoleCoordinator,
	}, t0)
	mustApply(t, s, &Op{
		Kind: OpRespond, Token: "th", MsgSerial: res["msg_serial"].(uint64),
		Disposition: "deny",
	}, t0)

	if s.Agents["asker"].IsCoordinator() {
		t.Error("a DENIED request granted the role")
	}
}

// Admin is never granted by pressing a button.
//
// Coordinator is breadth: broadcast and force_release, and it cannot read
// anybody's mail. Admin is the god view, every agent's decrypted mail included,
// and the entire reason the board sits behind Touch ID or a password is that
// reading everyone's mail is not a thing to hand over on a notification tapped
// between two others. Refused at the send, so the notification never exists.
func TestAdminCannotBeRequested(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	reg(t, s, "asker", "ta", t0)
	reg(t, s, "human", "th", t0)

	// Admit, not Apply. This is payload vocabulary, and a vocabulary rule
	// enforced in the fold is retroactive: the day the accepted set changes, the
	// daemon refuses to replay its own history. Asserting it here is asserting
	// it at the boundary it has to live at.
	if err := Admit(&Op{
		Kind: OpSendMessage, To: "human", MsgType: MsgRequest,
		Body: "I need to read everything", Grant: RoleAdmin,
	}, DefaultLimits()); err == nil {
		t.Fatal("admin was requestable: approving a notification would hand over " +
			"every agent's decrypted mail")
	}
	if s.Agents["asker"].IsAdmin() {
		t.Error("the agent holds admin")
	}
}

// A grant rides on a request, because a request is the only type with a yes.
//
// A notify carrying one would be a role change with no decision attached to it,
// and a question is answered with prose, so there is nothing for the grant to
// hang on.
func TestOnlyARequestCanCarryAGrant(t *testing.T) {
	for _, mt := range []string{MsgNotify, MsgQuestion, MsgHandoff} {
		// Admit, for the same reason as above.
		if err := Admit(&Op{
			Kind: OpSendMessage, To: "human", MsgType: mt,
			Body: "hi", Grant: RoleCoordinator,
		}, DefaultLimits()); err == nil {
			t.Errorf("a %s carried a grant: there is no approve on one, so the role "+
				"would change with no decision recorded anywhere", mt)
		}
	}
}

// Approving a reclaim request MOVES the mail.
//
// The board this was written against had dibs-maintainer, -2 and -3; codex-root
// and -2; codex-1 and -2. Every one is an agent that came back, could not prove
// it was itself, and started again beside its own unread mail. The recovery
// path existed and required an authority the returning agent could not have, so
// the only reachable action was to carry on as a sibling.
func TestApprovingAReclaimMovesTheMailbox(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	reg(t, s, "old", "told", t0)
	reg(t, s, "returned", "tr", t0)
	reg(t, s, "boss", "tb", t0)
	s.Agents["boss"].Role = RoleCoordinator

	// Mail arrives for the identity nobody can log back into.
	mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "tb", To: "old", MsgType: MsgNotify, Body: "stranded",
	}, t0)
	s.Agents["old"].Status = StatusDormant

	res := mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "tr", To: "boss", MsgType: MsgRequest,
		Body: "that was me before my harness restarted", Adopt: "old",
	}, t0)
	out := mustApply(t, s, &Op{
		Kind: OpRespond, Token: "tb", MsgSerial: res["msg_serial"].(uint64),
		Disposition: "approve", AdoptAuthorised: true,
	}, t0)

	if out["adopted"] != "old" {
		t.Errorf("the approval did not report an adoption: %v", out)
	}
	var landed bool
	for _, m := range s.Messages {
		if m.Body == "stranded" && m.To == "returned" {
			landed = true
		}
	}
	if !landed {
		t.Error("the stranded message is still addressed to the abandoned agent. An " +
			"approval that only records agreement leaves the mail exactly where " +
			"nobody can read it, which is the whole problem")
	}
}

// Approving one still needs the authority that performing one needs.
//
// Without this, any agent could be asked to approve a reclaim and the mailbox
// would move: two agents could hand each other anybody's mail.
func TestApprovingAReclaimStillNeedsTheAuthority(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	reg(t, s, "old", "told", t0)
	reg(t, s, "returned", "tr", t0)
	reg(t, s, "bystander", "tby", t0)
	s.Agents["old"].Status = StatusDormant

	res := mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "tr", To: "bystander", MsgType: MsgRequest,
		Body: "give me that mailbox", Adopt: "old",
	}, t0)
	// AdoptAuthorised is the engine's word, and it is false for an ordinary agent.
	if _, _, err := s.Apply(&Op{
		Kind: OpRespond, Token: "tby", MsgSerial: res["msg_serial"].(uint64),
		Disposition: "approve",
	}, t0); err == nil {
		t.Error("an unauthorised agent approved a reclaim and moved somebody's mailbox")
	}
}

// A role is a stable address even though the agents holding it are not.
func TestTheCoordinatorRoleResolvesToOneStableAgent(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	reg(t, s, "alpha", "ta", t0)
	reg(t, s, "beta", "tb", t0)
	if s.CoordinatorID() != "" {
		t.Fatal("setup: a board with no coordinator named one")
	}
	s.Agents["beta"].Role = RoleCoordinator
	s.Agents["alpha"].Role = RoleCoordinator

	// Map iteration is random, so the same board must not answer differently on
	// consecutive calls: mail addressed to a role would otherwise scatter across
	// whoever happened to come out first.
	first := s.CoordinatorID()
	for range 20 {
		if got := s.CoordinatorID(); got != first {
			t.Fatalf("the role resolved to %q and then %q: mail addressed to it would "+
				"land in different mailboxes on consecutive sends", first, got)
		}
	}
	// A live holder is preferred, because addressing a role has to reach
	// somebody who can answer.
	s.Agents["alpha"].Status = StatusDormant
	s.Agents["beta"].Status = StatusActive
	if got := s.CoordinatorID(); got != "beta" {
		t.Errorf("the role resolved to %q with a live holder available: on the board "+
			"this was written against, the standing coordinator was an agent nobody "+
			"could log back into", got)
	}
}
