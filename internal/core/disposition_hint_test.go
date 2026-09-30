package core

import (
	"errors"
	"strings"
	"testing"
)

// A wrong disposition is answered with one that works for that message.
//
// Every refusal here used to describe the disposition it rejected rather than
// the message in hand: approve on a question got "only requests take
// approve|deny", which says what not to do and leaves the agent to guess the
// rest. k7-dev hit it while driving two workers through the loop. The rule all
// errors in this repository are held to is that the hint IS the corrective
// call, so this takes the hint at its word: it pulls the first disposition the
// hint names, uses it, and requires it to succeed. A hint that names the right
// thing in prose and the wrong thing in practice fails here.
func TestEachDispositionRefusalNamesOneThatWorks(t *testing.T) {
	cases := []struct {
		msgType string
		wrong   string
		works   string // what the hint must lead to
	}{
		{MsgQuestion, "approve", "answer"},
		{MsgQuestion, "deny", "answer"},
		{MsgQuestion, "banana", "answer"},
		{MsgRequest, "answer", "approve"},
		{MsgRequest, "banana", "approve"},
	}
	for _, c := range cases {
		t.Run(c.msgType+"/"+c.wrong, func(t *testing.T) {
			s := NewState("hint", DefaultLimits())
			reg(t, s, "asker", "ta", t0)
			reg(t, s, "answerer", "tb", t0)
			sent := mustApply(t, s, &Op{
				Kind: OpSendMessage, Token: "ta", To: "answerer", MsgType: c.msgType, Body: "well?",
			}, t0)
			serial := sent["msg_serial"].(uint64)

			_, _, err := s.Apply(&Op{
				Kind: OpRespond, Token: "tb", MsgSerial: serial, Disposition: c.wrong, Body: "x",
			}, t0)
			var de *Error
			if !errors.As(err, &de) || de.Code != "E_BAD_DISPOSITION" {
				t.Fatalf("setup: %s on a %s returned %v, want E_BAD_DISPOSITION", c.wrong, c.msgType, err)
			}
			if !strings.Contains(de.Hint, c.works) {
				t.Fatalf("%s on a %s: the hint is %q, which does not name %q, the disposition "+
					"this message takes. An agent following the hint cannot get unstuck",
					c.wrong, c.msgType, de.Hint, c.works)
			}
			// Take the hint at its word.
			if _, _, err := s.Apply(&Op{
				Kind: OpRespond, Token: "tb", MsgSerial: serial, Disposition: c.works, Body: "done",
			}, t0); err != nil {
				t.Errorf("the hint named %q and using it failed: %v", c.works, err)
			}
		})
	}
}

// A notify or a handoff expects no response at all, and the hint says to ack it,
// which is the call that closes it.
func TestAResponseToANotifyIsPointedAtAck(t *testing.T) {
	for _, kind := range []string{MsgNotify, MsgHandoff} {
		s := NewState("hint", DefaultLimits())
		reg(t, s, "sender", "ts", t0)
		reg(t, s, "reader", "tr", t0)
		sent := mustApply(t, s, &Op{
			Kind: OpSendMessage, Token: "ts", To: "reader", MsgType: kind, Body: "fyi",
		}, t0)
		serial := sent["msg_serial"].(uint64)
		_, _, err := s.Apply(&Op{
			Kind: OpRespond, Token: "tr", MsgSerial: serial, Disposition: "decline", Body: "no",
		}, t0)
		var de *Error
		if !errors.As(err, &de) || !strings.Contains(de.Hint, "ack") {
			t.Fatalf("decline on a %s: %v; the hint should point at ack, the call that closes it", kind, err)
		}
		if _, _, err := s.Apply(&Op{Kind: OpAckMessage, Token: "tr", MsgSerial: serial}, t0); err != nil {
			t.Errorf("the hint said ack and ack failed on a %s: %v", kind, err)
		}
	}
}
