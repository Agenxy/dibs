package core

// dispositionHint names what THIS message takes, so the error for a wrong one
// is a call that works.
//
// Each refusal used to say what the rejected disposition was for ("only
// requests take approve|deny") rather than what the message in hand accepts, so
// an agent answering a question with approve was told what it could not do and
// left to guess the rest. k7-dev hit exactly that while driving two workers
// through the loop, and it is the rule every error here is held to: the hint is
// the corrective call. Built from the type, not listed per branch, so a branch
// cannot drift out of step with the others.
func dispositionHint(t string) string {
	switch t {
	case MsgQuestion:
		return "a question takes disposition answer, or decline if you will not answer it"
	case MsgRequest:
		return "a request takes disposition approve or deny, or decline if it is not yours to decide; " +
			"once you have approved it, done when the work is delivered"
	case MsgNotify, MsgHandoff:
		return "a " + t + " expects no response: close it with ack(msg_serial) instead of respond"
	}
	return "question takes answer|decline; request takes approve|deny|decline; notify and handoff take ack"
}
