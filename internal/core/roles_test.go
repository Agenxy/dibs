package core

import (
	"errors"
	"testing"
)

// TestCoordinatorIsGrantedNotClaimed: a lane must never be able to promote
// itself. Grant flows only through the admin op, which the engine admits solely
// on the human's admin path.
func TestCoordinatorIsGrantedNotClaimed(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	reg(t, s, "worker", "tw", t0)
	if s.Lanes["worker"].IsCoordinator() {
		t.Fatal("lanes must default to member")
	}
	// A lane using its own token cannot reach the grant op at all: grant_role is
	// handled before actor resolution and ignores tokens.
	mustApply(t, s, &Op{Kind: OpGrantRole, To: "worker", Mode: RoleCoordinator}, t0)
	if !s.Lanes["worker"].IsCoordinator() {
		t.Fatal("admin grant should promote the lane")
	}
	if _, _, err := s.Apply(&Op{Kind: OpGrantRole, To: "worker", Mode: "superuser"}, t0); err == nil {
		t.Fatal("unknown roles must be rejected")
	}
}

// TestAdminImpliesCoordinator: a higher tier that could not do what the tier
// below can would be a trap for whoever granted it.
func TestAdminImpliesCoordinator(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	reg(t, s, "boss", "tb", t0)
	mustApply(t, s, &Op{Kind: OpGrantRole, To: "boss", Mode: RoleAdmin}, t0)
	l := s.Lanes["boss"]
	if !l.IsAdmin() {
		t.Fatal("admin role not set")
	}
	if !l.IsCoordinator() {
		t.Fatal("admin must also hold coordinator powers")
	}
	// And a coordinator is NOT an admin: the escalation is one-way.
	reg(t, s, "lead", "tl", t0)
	mustApply(t, s, &Op{Kind: OpGrantRole, To: "lead", Mode: RoleCoordinator}, t0)
	if s.Lanes["lead"].IsAdmin() {
		t.Fatal("coordinator must not silently gain admin (mail-reading) power")
	}
}

// TestForceReleaseNeedsCoordinator: unsticking someone else's claim is a real
// privilege, and it must be visible to the holder when it happens.
func TestForceReleaseNeedsCoordinator(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	reg(t, s, "holder", "th", t0)
	reg(t, s, "boss", "tb", t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "th"}, t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "tb"}, t0)
	mustApply(t, s, &Op{Kind: OpClaim, Token: "th", Path: "/dev/ttyUSB0", Mode: ClaimExclusive}, t0)

	// A peer cannot.
	if _, _, err := s.Apply(&Op{Kind: OpForceRelease, Token: "tb", Path: "/dev/ttyUSB0"}, t0); !errors.Is(err, ErrNotCoordinator) {
		t.Fatalf("non-coordinator force_release: got %v, want E_NOT_COORDINATOR", err)
	}
	// A coordinator can, and the holder is told.
	mustApply(t, s, &Op{Kind: OpGrantRole, To: "boss", Mode: RoleCoordinator}, t0)
	_, evs, err := s.Apply(&Op{Kind: OpForceRelease, Token: "tb", Path: "/dev/ttyUSB0", Note: "holder died"}, t0)
	if err != nil {
		t.Fatalf("coordinator force_release: %v", err)
	}
	var told bool
	for _, e := range evs {
		if e.Type == "claim.force_released" && e.To == "holder" {
			told = true
		}
	}
	if !told {
		t.Fatal("the claim holder must be notified: silent seizure is not honest")
	}
	if len(s.Claims) != 0 {
		t.Fatal("claim should be gone")
	}
}

// TestCoordinatorCannotReadOthersMail is the boundary that keeps the founding
// guarantee true for the common case. Breadth, not intrusion: an admin, which
// the human grants deliberately and separately, is the documented exception.
func TestCoordinatorCannotReadOthersMail(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	reg(t, s, "a", "ta", t0)
	reg(t, s, "b", "tb", t0)
	reg(t, s, "boss", "tboss", t0)
	mustApply(t, s, &Op{Kind: OpGrantRole, To: "boss", Mode: RoleCoordinator}, t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "ta"}, t0)
	res := mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "ta", To: "b",
		MsgType: MsgNotify, Body: "private between a and b",
	}, t0)
	serial := res["msg_serial"].(uint64)

	// The coordinator is not a participant, so the message is not in its inbox.
	for _, m := range s.Inbox("boss") {
		if m.Serial == serial {
			t.Fatal("coordinator must not receive other lanes' mail")
		}
	}
	// And access is still participant-scoped: only a and b.
	m := s.Messages[serial]
	if m.From != "a" || m.To != "b" {
		t.Fatalf("unexpected participants: %+v", m)
	}
}
