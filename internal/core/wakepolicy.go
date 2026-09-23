package core

// WakeWorthy reports whether an event about mail to an agent justifies
// interrupting that agent: starting a process on the operator's machine, or
// putting a notice into a running session. ONE rule, read by the daemon's
// waker for both of its routes and by the bridge's self-wake, which learns
// what arrived from the notification's _meta. Two copies of it disagreed: the
// bridge woke a session for every inbox change, and a notify is by definition
// news nobody is blocked on, read at the next check_in. Found by the
// pre-release review, round four.
//
// A VERDICT is always blocking and carries no msg_type: it is the answer to
// something this agent asked and then stopped for. Filtering verdicts by a
// field they do not have dropped every one of them, which is how the first
// version of the waker excluded exactly the case with the strongest claim on
// a wake.
func WakeWorthy(evType, msgType string) bool {
	return IsMailEvent(evType) && (!urgentOnly || Blocking(evType, msgType))
}

// urgentOnly is off: MAIL WAKES AN AGENT, which is the product.
//
// This rule used to be "only news somebody is blocked on", so a notify never
// started anybody. The two routes then disagreed with each other, which is
// what gave the game away: the hook path asks `WakePolicy`, whose default is
// `all` and which delivers a notify at a turn boundary, while this path
// hard-coded the `urgent` behaviour and ignored the operator's setting
// entirely. One question, two answers, and the quieter one won wherever it
// mattered most: an agent that had stopped.
//
// The reasoning for the old default was that a notify costs a subprocess and
// nobody is blocked on it. That is a real cost and it is the operator's to
// weigh, which is what `[wake] policy = "urgent"` is for. It is not a reason
// for the service to decide that some mail does not arrive: a coordination
// board whose messages reach an agent only when its human happens to type is
// the failure Dibs exists to remove, and it was measured doing exactly that.
//
// Kept as a named constant rather than deleted, because `Blocking` below is
// still the rule `urgent` selects with, and the two must not drift apart
// again.
const urgentOnly = false

// IsMailEvent reports whether an event is mail arriving for an agent, as
// opposed to anything else the board publishes.
func IsMailEvent(evType string) bool {
	switch evType {
	case "message.approved", "message.denied", "message.answered", "message.declined",
		"message.sent", "message.adopted":
		return true
	}
	return false
}

// Blocking reports whether somebody is waiting on this mail: the narrower set
// `[wake] policy = "urgent"` selects, and what `deliverToModel` already means
// by its own `blocked`.
//
// A VERDICT is always blocking and carries no msg_type: it is the answer to
// something this agent asked and then stopped for. Filtering verdicts by a
// field they do not have dropped every one of them, which is how the first
// version of the waker excluded exactly the case with the strongest claim on
// a wake.
func Blocking(evType, msgType string) bool {
	switch evType {
	case "message.approved", "message.denied", "message.answered", "message.declined":
		return true
	case "message.sent", "message.adopted":
		// Recovered mail is arrived mail: an adoption that moved a pending
		// question into an agent wakes that agent as a send would.
		switch msgType {
		case MsgQuestion, MsgRequest, MsgHandoff:
			return true
		}
	}
	return false
}
