package core

import (
	"errors"
	"testing"
	"time"
)

// A request whose adopt named an agent outlived that agent's purge, and
// approval resolves the name against the roster of the day: approved after
// a stranger had registered the released name, it moved the stranger's
// mail. The purge expires such requests.
func TestAPurgeExpiresARequestToAdoptThePurgedMailbox(t *testing.T) {
	s := NewState("probe", DefaultLimits())
	now := time.Unix(1700000000, 0)
	ackReg(t, s, "boss", "tok-b", now)
	s.Agents["boss"].Role = RoleCoordinator
	ackReg(t, s, "requester", "tok-r", now)
	ackReg(t, s, "lost", "tok-l", now)
	s.Agents["lost"].Status = StatusDormant
	req := mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "tok-r", To: "boss", MsgType: MsgRequest,
		Body: "that mailbox is mine", Adopt: "lost", V7Semantics: true,
	}, now)
	serial, _ := req["msg_serial"].(uint64)
	// The purge.
	s.Agents["lost"].Status = StatusArchived
	s.Agents["lost"].ArchivedAt = now.Add(-s.Limits.ArchiveRetention - time.Hour)
	mustApply(t, s, &Op{Kind: OpSweep, PurgeMail: true, V7Semantics: true}, now)
	if s.Agents["lost"] != nil {
		t.Fatal("setup: the sweep did not purge lost")
	}
	m := s.Messages[serial]
	if m == nil || !m.Terminal() {
		t.Fatalf("the request to adopt the purged mailbox is still standing: %+v", m)
	}
	// A stranger takes the name, with mail of its own.
	regPersistent(t, s, "lost", "tok-l2", "n-lost-2-0123456789", now.Add(time.Hour))
	mustApply(t, s, &Op{Kind: OpSendMessage, Token: "tok-b", To: "lost", MsgType: MsgNotify, Body: "private", V7Semantics: true}, now.Add(time.Hour))
	s.Agents["lost"].Status = StatusDormant
	// Approving the old request must not move the stranger's mail.
	_, _, err := s.Apply(&Op{Kind: OpRespond, Token: "tok-b", MsgSerial: serial, Disposition: "approve", AdoptAuthorised: true, V7Semantics: true}, now.Add(2*time.Hour))
	var ce *Error
	if err == nil || !errors.As(err, &ce) {
		t.Fatalf("approving a request that named a purged mailbox was accepted: %v", err)
	}
	if n := len(s.Inbox("lost")); n != 1 {
		t.Fatalf("the stranger's mailbox holds %d message(s), want its own 1: the approval moved it", n)
	}
}
