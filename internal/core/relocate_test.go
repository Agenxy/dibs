package core

import (
	"errors"
	"testing"
)

// Moving an agent to another environment is a permission, not something any
// agent can do: the human always may, a coordinator or admin may by role, and
// anybody else only once the human has granted it. The operator asked for
// exactly that after finding their ChatGPT threads run elsewhere without
// anybody choosing it: a wake never moves an agent, and a move is somebody's
// recorded decision.
func TestOnlyAPermittedAgentMayRelocateAnother(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	reg(t, s, "target", "tt", t0)
	reg(t, s, "member", "tm", t0)
	reg(t, s, "lead", "tl", t0)
	mustApply(t, s, &Op{Kind: OpGrantRole, To: "lead", Mode: RoleCoordinator}, t0)
	serial := s.Serial

	_, _, err := s.Apply(&Op{Kind: OpRelocate, Token: "tm", To: "target", Mode: "headless"}, t0)
	if !errors.Is(err, ErrNotPermittedToRelocate) {
		t.Fatalf("a plain member moved another agent (err %v): every agent on the board "+
			"could run anybody's thread anywhere", err)
	}
	if s.Serial != serial || s.Agents["target"].Relocated != nil {
		t.Fatal("a refused relocation changed state, so it would have been ledgered")
	}

	res := mustApply(t, s, &Op{Kind: OpRelocate, Token: "tl", To: "target", Mode: "headless"}, t0)
	r := s.Agents["target"].Relocated
	if r == nil || r.By != "lead" || r.To != "headless" || res["by"] != "lead" {
		t.Fatalf("a coordinator's relocation recorded %+v: the board cannot say who "+
			"moved the agent or where", r)
	}

	mustApply(t, s, &Op{Kind: OpGrantPermission, To: "member", Mode: PermRelocate}, t0)
	mustApply(t, s, &Op{Kind: OpRelocate, Token: "tm", To: "target", Mode: "chatgpt-app"}, t0)
	if r := s.Agents["target"].Relocated; r == nil || r.By != "member" {
		t.Fatalf("a member the human granted relocate could not use it: %+v", r)
	}
	if s.Agents["member"].Role != "" && s.Agents["member"].Role != RoleMember {
		t.Error("granting relocate changed the agent's role: it is a permission beside the role")
	}

	mustApply(t, s, &Op{Kind: OpRevokePermission, To: "member", Mode: PermRelocate}, t0)
	if _, _, err := s.Apply(&Op{Kind: OpRelocate, Token: "tm", To: "target", Mode: "x"}, t0); !errors.Is(err, ErrNotPermittedToRelocate) {
		t.Errorf("a revoked permission still worked: %v", err)
	}

	mustApply(t, s, &Op{Kind: OpRelocateByHuman, To: "target", Mode: "headless"}, t0)
	if r := s.Agents["target"].Relocated; r == nil || r.By != HumanActor {
		t.Errorf("the human's relocation recorded %+v", r)
	}
}

// A grant that changes nothing is not a transition: no event, no serial, so
// the engine does not ledger it (AGENTS.md rule 2).
func TestARepeatedGrantIsNotLedgered(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	reg(t, s, "member", "tm", t0)
	mustApply(t, s, &Op{Kind: OpGrantPermission, To: "member", Mode: PermRelocate}, t0)
	serial := s.Serial
	_, evs, err := s.Apply(&Op{Kind: OpGrantPermission, To: "member", Mode: PermRelocate}, t0)
	if err != nil || len(evs) != 0 || s.Serial != serial {
		t.Errorf("granting a held permission again: err=%v events=%d serial %d->%d", err, len(evs), serial, s.Serial)
	}
	if len(s.Agents["member"].Permissions) != 1 {
		t.Errorf("permissions = %v: a repeated grant was stored twice", s.Agents["member"].Permissions)
	}
	_, evs, _ = s.Apply(&Op{Kind: OpRevokePermission, To: "member", Mode: PermRelocate}, t0)
	if len(evs) != 1 || s.Agents["member"].Permissions != nil {
		t.Errorf("revoking: events=%d permissions=%v", len(evs), s.Agents["member"].Permissions)
	}
}

// Asking the human is the ordinary route, and their yes IS the grant, the way
// a coordinator request works: the permission lands, the role does not move.
func TestApprovingARelocateRequestGrantsThePermission(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	reg(t, s, "asker", "ta", t0)
	reg(t, s, "person", "tp", t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "ta"}, t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "tp"}, t0)
	op := &Op{
		Kind: OpSendMessage, Token: "ta", To: "person", MsgType: MsgRequest,
		Body: "may I move the worker into the app?", Grant: PermRelocate, OpID: "r1",
	}
	if err := Admit(op, DefaultLimits()); err != nil {
		t.Fatalf("a request for relocate was refused at ingress: %v", err)
	}
	res := mustApply(t, s, op, t0)
	serial, _ := res["msg_serial"].(uint64)
	if serial == 0 {
		t.Fatalf("setup: no message serial in %v", res)
	}
	out := mustApply(t, s, &Op{Kind: OpRespond, Token: "tp", MsgSerial: serial, Disposition: "approve"}, t0)
	a := s.Agents["asker"]
	if !a.HasPermission(PermRelocate) {
		t.Fatal("the approved request did not grant relocate: the yes recorded an agreement and did nothing")
	}
	if a.Role != "" && a.Role != RoleMember {
		t.Errorf("approving relocate made the agent %q", a.Role)
	}
	if out["granted"] != nil {
		t.Error("the permission was reported as a granted ROLE, which the engine pins against dibs.toml")
	}
}

// Vocabulary is checked at ingress, never in the fold.
func TestAnUnknownPermissionIsRefusedAtIngress(t *testing.T) {
	for _, op := range []*Op{
		{Kind: OpGrantPermission, To: "a", Mode: "read_everything"},
		{Kind: OpRelocate, To: "a"},
		{Kind: OpRelocateByHuman, Mode: "headless"},
	} {
		if err := Admit(op, DefaultLimits()); err == nil {
			t.Errorf("Admit accepted %+v", op)
		}
	}
}
