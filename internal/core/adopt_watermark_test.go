package core

import (
	"testing"
	"time"
)

// Adoption does not hand over mail the source was told was not its own.
//
// TruncatedBefore is the watermark that stops a name coming back from being
// handed the previous occupant's mail: an id is derived from the name, so a
// returning name reuses the id, and a sweep written before v0.0.7 removes the
// row while keeping the messages. Inbox filters on it. Both adoption paths read
// every message matching the id and readdressed it, so the one route that
// exists to RECOVER an abandoned mailbox was also the one route that disclosed
// the mail that mailbox had already been excluded from.
//
// What makes it worse than an ordinary leak: the approver is shown a count.
// Nothing in the request says that some of those messages were addressed to
// somebody else entirely, so the human authorising it cannot see what they are
// granting. Authorising the recovery of an identity is not authorising the
// disclosure of its predecessor's mail.
//
// Found by the pre-release review, which reproduced it; this is its probe,
// extended to the approved path, which had the same loop written out a second
// time.
func TestAdoptionDoesNotExposeMailBelowTheSourceWatermark(t *testing.T) {
	for _, tc := range []struct {
		name  string
		adopt func(t *testing.T, s *State, now time.Time)
	}{
		{"direct", func(t *testing.T, s *State, now time.Time) {
			t.Helper()
			mustApply(t, s, &Op{
				Kind: OpAdoptAgent, Token: "tok-a", To: "target", AdoptAuthorised: true,
			}, now)
		}},
		// The same rule, written out a second time in the approval path. Both
		// cases run because fixing one of two copies is how this survived.
		{"approved request", func(t *testing.T, s *State, now time.Time) {
			t.Helper()
			s.Agents["sender"].Role = RoleCoordinator
			req := mustApply(t, s, &Op{
				Kind: OpSendMessage, Token: "tok-a", To: "sender", MsgType: MsgRequest,
				Body: "that mailbox was mine", Adopt: "target",
			}, now)
			mustApply(t, s, &Op{
				Kind: OpRespond, Token: "tok-s", MsgSerial: req["msg_serial"].(uint64),
				Disposition: "approve", AdoptAuthorised: true,
			}, now)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewState("probe", DefaultLimits())
			now := time.Unix(1700000000, 0)
			reg(t, s, "sender", "tok-s", now)
			reg(t, s, "target", "tok-old", now)
			hidden := mustApply(t, s, &Op{
				Kind: OpSendMessage, Token: "tok-s", To: "target",
				MsgType: MsgQuestion, Body: "PREDECESSOR SECRET", DeadlineSec: 60,
			}, now)["msg_serial"].(uint64)

			// The v0.0.6 state this exists for: the row was purged and the
			// expired message kept, so its SENDER can still see why it went
			// unanswered.
			delete(s.Agents, "target")
			mustApply(t, s, &Op{Kind: OpSweep, PurgeMail: true}, now.Add(2*time.Hour))
			if s.Messages[hidden] == nil {
				t.Fatal("setup: the predecessor message did not survive the sweep, " +
					"so this fixture is not reproducing anything")
			}

			// The name comes back. Registering raises the watermark above the
			// mail that was addressed to the previous occupant.
			mustApply(t, s, &Op{
				Kind: OpRegister, Name: "target", NewToken: "tok-new", V7Semantics: true,
			}, now.Add(3*time.Hour))
			visible := mustApply(t, s, &Op{
				Kind: OpSendMessage, Token: "tok-s", To: "target",
				MsgType: MsgNotify, Body: "CURRENT MAIL",
			}, now.Add(4*time.Hour))["msg_serial"].(uint64)
			if got := s.Inbox("target"); len(got) != 1 || got[0].Serial != visible {
				t.Fatalf("setup: the source must see exactly its own mail, saw %#v", got)
			}

			reg(t, s, "adopter", "tok-a", now.Add(4*time.Hour))
			mustApply(t, s, &Op{Kind: OpSignOff, Token: "tok-new"}, now.Add(5*time.Hour))
			tc.adopt(t, s, now.Add(6*time.Hour))

			var sawVisible bool
			for _, m := range s.Inbox("adopter") {
				if m.Serial == hidden {
					t.Fatalf("adoption exposed predecessor mail #%d with body %q. "+
						"The source could not read it; adopting the source does not "+
						"make it readable", m.Serial, m.Body)
				}
				if m.Serial == visible {
					sawVisible = true
				}
			}
			// The refusal has to be narrow, or this test passes against an
			// adoption that moves nothing at all.
			if !sawVisible {
				t.Errorf("the adopter did not receive the source's OWN mail #%d: "+
					"adoption must still recover the mailbox it was authorised for", visible)
			}
		})
	}
}
