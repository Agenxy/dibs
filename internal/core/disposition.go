// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import "fmt"

func questionResponseHint(m *Message, fallback string) string {
	if m.Type == MsgQuestion {
		return dispositionHint(m.Type, m.Serial)
	}
	return fallback
}

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
func dispositionHint(t string, serial uint64) string {
	switch t {
	case MsgQuestion:
		return fmt.Sprintf("respond(msg_serial:%d, disposition:\"answer\", body:...); "+
			"use disposition:\"decline\" if you will not answer it", serial)
	case MsgRequest:
		return fmt.Sprintf("respond(msg_serial:%d, disposition:\"approve\") means I'll do it; "+
			"use deny or decline if you will not take it, then disposition:\"done\" once delivered", serial)
	case MsgNotify, MsgHandoff:
		return fmt.Sprintf("a %s expects no response: ack(msg_serial:%d) instead of respond", t, serial)
	}
	return "question takes answer|decline; request takes approve|deny|decline; notify and handoff take ack"
}
