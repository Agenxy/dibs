package core

import "testing"

// MAIL WAKES AN AGENT. That is the product, and it is what this used to get
// wrong.
//
// The rule here was "only blocking mail is worth a wake", so a notify started
// nobody. The daemon's other route disagreed: `deliverToModel` asks the
// operator's `[wake] policy`, whose default is `all`, and delivers a notify at
// a turn boundary. One question, two answers, and the routes are not
// interchangeable, because the hook path can only reach an agent that is still
// running. The route that said no was the only one that could reach an agent
// that had stopped, which is the whole reason Dibs exists.
//
// Measured on a live board: a peer sent substantial feedback as a notify at
// 21:10:25, the recipient was idle, nothing was attempted, nothing was logged,
// and the agent learned of it an hour later because its operator mentioned it.
//
// Which mail wakes is still a choice, and it is the operator's: `[wake]
// policy = "urgent"` narrows it to `Blocking` below. It is not the service's
// to make quietly on their behalf.
func TestMailWakesAnAgent(t *testing.T) {
	cases := []struct {
		ev, msg string
		want    bool
	}{
		{"message.sent", MsgNotify, true}, // the case this test was written for
		{"message.sent", MsgQuestion, true},
		{"message.sent", MsgRequest, true},
		{"message.sent", MsgHandoff, true},
		{"message.answered", "", true}, // a verdict has no msg_type
		{"message.approved", "", true},
		{"message.denied", "", true},
		{"message.declined", "", true},
		{"message.adopted", MsgQuestion, true},
		{"message.adopted", MsgNotify, true},
		// Still not everything the board publishes: an ack is mail LEAVING,
		// and an agent registering is not addressed to anybody.
		{"message.acked", MsgQuestion, false},
		{"agent.registered", "", false},
	}
	for _, c := range cases {
		if got := WakeWorthy(c.ev, c.msg); got != c.want {
			t.Errorf("WakeWorthy(%q, %q) = %v, want %v", c.ev, c.msg, got, c.want)
		}
	}
}

// And the narrower rule survives intact, because `urgent` selects with it and
// the two must not drift apart: an operator who says "only interrupt me for
// something somebody is blocked on" is asking for exactly this set.
func TestBlockingIsStillTheUrgentSubset(t *testing.T) {
	blocking := []struct{ ev, msg string }{
		{"message.sent", MsgQuestion},
		{"message.sent", MsgRequest},
		{"message.sent", MsgHandoff},
		{"message.answered", ""},
		{"message.approved", ""},
		{"message.denied", ""},
		{"message.declined", ""},
		{"message.adopted", MsgQuestion},
	}
	for _, c := range blocking {
		if !Blocking(c.ev, c.msg) {
			t.Errorf("Blocking(%q, %q) = false: somebody is waiting on this", c.ev, c.msg)
		}
	}
	// A notify is the whole difference between the two rules. If this ever
	// returns true they have collapsed into one and `urgent` means nothing.
	inert := []struct{ ev, msg string }{
		{"message.sent", MsgNotify},
		{"message.adopted", MsgNotify},
	}
	for _, c := range inert {
		if Blocking(c.ev, c.msg) {
			t.Errorf("Blocking(%q, %q) = true: nobody is blocked on a notify, and "+
				"`urgent` exists to skip exactly these", c.ev, c.msg)
		}
	}
	// A notify is mail even so, which is why it wakes under the default.
	if !IsMailEvent("message.sent") || IsMailEvent("agent.registered") {
		t.Error("IsMailEvent has to separate mail from everything else the board publishes")
	}
}
