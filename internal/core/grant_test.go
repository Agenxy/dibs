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

	_, _, err := s.Apply(&Op{
		Kind: OpSendMessage, Token: "ta", To: "human", MsgType: MsgRequest,
		Body: "I need to read everything", Grant: RoleAdmin,
	}, t0)
	if err == nil {
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
		s := NewState("n1", DefaultLimits())
		reg(t, s, "asker", "ta", t0)
		reg(t, s, "human", "th", t0)
		if _, _, err := s.Apply(&Op{
			Kind: OpSendMessage, Token: "ta", To: "human", MsgType: mt,
			Body: "hi", Grant: RoleCoordinator,
		}, t0); err == nil {
			t.Errorf("a %s carried a grant: there is no approve on one, so the role "+
				"would change with no decision recorded anywhere", mt)
		}
	}
}
