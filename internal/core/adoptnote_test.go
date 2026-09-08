package core

import (
	"strings"
	"testing"
)

// A mailbox moves by two routes, and both have to describe what they did.
//
// `adopt_agent` is one. Approving a `request` that carries `adopt` is the
// other, and it is the route a returning agent is actually told to take: the
// name_taken hint on register says to ask a coordinator, not to call
// adopt_agent. So the approval path is the one a stranded agent is most likely
// to arrive by, and it was the one still saying "only where its mail is
// delivered has changed".
//
// That sentence is true of the messages it moved and reads as a standing rule,
// and a coordinator that adopted three mailboxes read it as one: it announced
// that it had become the delivery address for that NAME and would hand the
// address back if the original returned. Round forty-five fixed the wording on
// adopt_agent and left this copy alone, so the fix reached one of two doors.
//
// Asserted as a PROPERTY of both results rather than as one pinned string. The
// defect was two hand-written notes drifting apart; a test that pins the text
// of each would go green on the day somebody edits both to say the wrong thing
// in unison, and pinning one exact sentence is how a guard turns into a
// spelling test.
func TestBothAdoptionRoutesDescribeAOneTimeMove(t *testing.T) {
	// Route one: adopt_agent directly.
	direct := func() string {
		s := NewState("direct", DefaultLimits())
		reg(t, s, "sender", "ts", t0)
		regPersistent(t, s, "lost", "tl", "n-lost", t0)
		heir := reg(t, s, "heir", "th", t0)
		mustApply(t, s, &Op{Kind: OpAckBoard, Token: heir.Token}, t0)
		mustApply(t, s, &Op{
			Kind: OpSendMessage, Token: "ts", To: "lost", MsgType: MsgNotify, Body: "stranded",
		}, t0)
		s.Agents["lost"].Status = StatusDormant
		res := mustApply(t, s, &Op{
			Kind: OpAdoptAgent, Token: heir.Token, To: "lost", AdoptAuthorised: true,
		}, t0)
		note, _ := res["note"].(string)
		return note
	}()

	// Route two: a coordinator approving a request that carries `adopt`.
	approved := func() string {
		s := NewState("approved", DefaultLimits())
		reg(t, s, "lost", "tl", t0)
		reg(t, s, "returned", "tr", t0)
		reg(t, s, "boss", "tb", t0)
		s.Agents["boss"].Role = RoleCoordinator
		mustApply(t, s, &Op{
			Kind: OpSendMessage, Token: "tb", To: "lost", MsgType: MsgNotify, Body: "stranded",
		}, t0)
		s.Agents["lost"].Status = StatusDormant
		req := mustApply(t, s, &Op{
			Kind: OpSendMessage, Token: "tr", To: "boss", MsgType: MsgRequest,
			Body: "that mailbox is mine", Adopt: "lost",
		}, t0)
		out := mustApply(t, s, &Op{
			Kind: OpRespond, Token: "tb", MsgSerial: req["msg_serial"].(uint64),
			Disposition: "approve", AdoptAuthorised: true,
		}, t0)
		if out["adopted"] != "lost" {
			t.Fatalf("setup: the approval did not adopt, so the note below is not "+
				"the one under test: %v", out)
		}
		note, _ := out["adopt_note"].(string)
		return note
	}()

	for route, note := range map[string]string{
		"adopt_agent":      direct,
		"approved request": approved,
	} {
		if note == "" {
			t.Errorf("%s returned no note at all: the caller is told a mailbox moved "+
				"and nothing about what that means", route)
			continue
		}
		// The dangerous reading is that delivery has been re-pointed from now on.
		// A note that says a redirect happened, and does not say it is not one, is
		// the wording that produced a coordinator announcing itself as the standing
		// address for somebody else's name.
		if !strings.Contains(note, "not a standing redirect") {
			t.Errorf("%s: the note does not rule out a standing redirect, which is "+
				"the reading that has already been taken from it once.\n  note: %s",
				route, note)
		}
		// And it has to say the source keeps receiving, because that is the fact
		// an adopter needs in order not to intercept a live agent.
		if !strings.Contains(note, "reaches") || !strings.Contains(note, "comes back") {
			t.Errorf("%s: the note does not say that mail sent from here on still "+
				"reaches the source when it returns.\n  note: %s", route, note)
		}
	}

	if direct != approved {
		t.Errorf("the two adoption routes describe the same operation differently, "+
			"which is how one of them came to be wrong for a release.\n"+
			"  adopt_agent:      %s\n  approved request: %s", direct, approved)
	}
}
