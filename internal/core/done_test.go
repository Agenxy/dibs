package core

import (
	"errors"
	"testing"
)

func approvedRequest(t *testing.T) (*State, uint64) {
	t.Helper()
	s := NewState("n1", DefaultLimits())
	reg(t, s, "lead", "tl", t0)
	reg(t, s, "worker", "tw", t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "tl"}, t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "tw"}, t0)
	res := mustApply(t, s, &Op{Kind: OpSendMessage, Token: "tl", To: "worker", MsgType: MsgRequest, Body: "build C", OpID: "r1"}, t0)
	serial, _ := res["msg_serial"].(uint64)
	if serial == 0 {
		t.Fatalf("setup: no serial in %v", res)
	}
	mustApply(t, s, &Op{Kind: OpRespond, Token: "tw", MsgSerial: serial, Disposition: "approve"}, t0)
	return s, serial
}

// Approval said "I will"; done says "I have", and tells the requester. Until
// it, the request is an obligation the daemon holds the agent to.
func TestARequestApprovedAndDeliveredIsReportedDone(t *testing.T) {
	s, serial := approvedRequest(t)
	_, evs, err := s.Apply(&Op{Kind: OpRespond, Token: "tw", MsgSerial: serial, Disposition: "done", Body: "pr:1700"}, t0)
	if err != nil {
		t.Fatalf("done on an approved request: %v", err)
	}
	m := s.Messages[serial]
	if m.State != MsgStateDone || !m.Terminal() {
		t.Errorf("state %q after done", m.State)
	}
	if len(evs) != 1 || evs[0].Type != "message.done" || evs[0].To != "lead" {
		t.Errorf("events %+v: the requester is the one waiting on this", evs)
	}
	if !IsMailEvent("message.done") || !Blocking("message.done", "") {
		t.Error("message.done does not wake the requester: it is a verdict somebody is waiting on")
	}
	if _, _, err := s.Apply(&Op{Kind: OpRespond, Token: "tw", MsgSerial: serial, Disposition: "done"}, t0); err == nil {
		t.Error("a request was reported done twice")
	}
}

func TestDoneIsOnlyForARequestYouApproved(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	reg(t, s, "lead", "tl", t0)
	reg(t, s, "worker", "tw", t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "tl"}, t0)
	pending := mustApply(t, s, &Op{Kind: OpSendMessage, Token: "tl", To: "worker", MsgType: MsgRequest, Body: "x", OpID: "a"}, t0)["msg_serial"].(uint64)
	question := mustApply(t, s, &Op{Kind: OpSendMessage, Token: "tl", To: "worker", MsgType: MsgQuestion, Body: "y", OpID: "b"}, t0)["msg_serial"].(uint64)
	for name, serial := range map[string]uint64{"a request not yet approved": pending, "a question": question} {
		_, _, err := s.Apply(&Op{Kind: OpRespond, Token: "tw", MsgSerial: serial, Disposition: "done"}, t0)
		var e *Error
		if !errors.As(err, &e) || e.Code != "E_BAD_DISPOSITION" || e.Hint == "" {
			t.Errorf("%s: done gave %v, want E_BAD_DISPOSITION with a hint", name, err)
		}
	}
}
