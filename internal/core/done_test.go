package core

import (
	"errors"
	"strings"
	"testing"
	"time"
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

// The reported sequence: request, approve, then done a while later. Responding
// marks a message consumed, and the sweep deletes consumed finished mail after
// fifteen minutes, so done found nothing (E_NO_MESSAGE) on both requests a
// worker tried to close (k7-dev, Dibs #2130). A sweep that says keep_owed
// keeps a request still owed; one recorded before that flag existed replays as
// it ran.
func TestDoneStillWorksAfterTheSweepThatFollowsApproval(t *testing.T) {
	later := t0.Add(20 * time.Minute)

	s, serial := approvedRequest(t)
	mustApply(t, s, &Op{Kind: OpSweep, PurgeMail: true, V7Semantics: true, KeepOwed: true}, later)
	if _, _, err := s.Apply(&Op{Kind: OpRespond, Token: "tw", MsgSerial: serial, Disposition: "done", Body: "delivered"}, later); err != nil {
		t.Fatalf("done after the sweep: %v (the documented completion path does not work)", err)
	}
	if s.Messages[serial].State != MsgStateDone {
		t.Errorf("state %q", s.Messages[serial].State)
	}

	old, oldSerial := approvedRequest(t)
	mustApply(t, old, &Op{Kind: OpSweep, PurgeMail: true, V7Semantics: true}, later)
	if _, kept := old.Messages[oldSerial]; kept {
		t.Error("a sweep recorded before keep_owed kept the approved request: replay would " +
			"rebuild a board its own daemon never held")
	}

	// And past the window it is history, kept or not.
	gone, goneSerial := approvedRequest(t)
	mustApply(t, gone, &Op{Kind: OpSweep, PurgeMail: true, V7Semantics: true, KeepOwed: true},
		t0.Add(ObligationWindow+time.Hour))
	_, _, err := gone.Apply(&Op{Kind: OpRespond, Token: "tw", MsgSerial: goneSerial, Disposition: "done"},
		t0.Add(ObligationWindow+time.Hour))
	var e *Error
	if !errors.As(err, &e) || e.Code != "E_NO_MESSAGE" || !strings.Contains(e.Hint, "for a day") {
		t.Errorf("done on a request past the window: %v, want E_NO_MESSAGE saying why", err)
	}
}
