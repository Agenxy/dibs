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
	switch evType {
	case "message.approved", "message.denied", "message.answered", "message.declined":
		return true
	case "message.sent":
		switch msgType {
		case MsgQuestion, MsgRequest, MsgHandoff:
			return true
		}
	}
	return false
}
