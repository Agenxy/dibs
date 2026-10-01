package core

import "time"

// applyDone closes a request its recipient approved: the work it accepted has
// been delivered.
//
// Approval said "I will". Nothing said "I have", so an accepted request was
// indistinguishable from a finished one and a worker that accepted work and
// stopped looked exactly like one that had done it. Part E of the design
// agreed with k7-dev (Dibs #1129): until done, an approved request is an open
// obligation, which the daemon treats as declared work (engine/obligations.go),
// and done tells the requester, who is usually waiting on exactly this.
//
// State rules, so in the fold and not at ingress: they read the message.
// "done" was refused as an unknown disposition by every earlier build, so no
// ledger holds one and nothing here is retroactive.
func (s *State) applyDone(m *Message, op *Op, now time.Time) (Result, []Event, error) {
	if m.Type != MsgRequest || m.State != MsgStateApproved {
		state := m.State
		if state == "" {
			state = "pending"
		}
		return nil, nil, errf("E_BAD_DISPOSITION",
			"done closes a request you APPROVED, once the work is delivered: approve it first, "+
				"or answer a question with answer",
			"message %d is a %s and %s, not an approved request", m.Serial, m.Type, state)
	}
	if m.Grant != "" || m.Adopt != "" {
		return nil, nil, errf("E_BAD_DISPOSITION",
			"approving it already performed it: there is no work left to report",
			"message %d carried its own effect", m.Serial)
	}
	if len(op.Body) > s.Limits.MaxBodyBytes {
		return nil, nil, errTooLarge("response body", s.Limits.MaxBodyBytes)
	}
	m.State = MsgStateDone
	if op.Body != "" {
		if m.Response != "" {
			m.Response += "\n\n"
		}
		m.Response += "done: " + op.Body
	}
	m.TerminalAt = now
	m.RespondedAt = s.Serial + 1
	evs := []Event{{Type: "message." + MsgStateDone, Agent: m.To, To: m.From, Data: map[string]any{
		"msg_serial": m.Serial,
	}}}
	s.finish(&evs, now)
	return Result{"ok": true, "state": MsgStateDone}, evs, nil
}
