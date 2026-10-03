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
	m.Deliverable = op.Deliverable
	if op.Body != "" {
		if m.Response != "" {
			m.Response += "\n\n"
		}
		m.Response += "done: " + op.Body
	}
	m.TerminalAt = now
	m.RespondedAt = s.Serial + 1
	data := map[string]any{"msg_serial": m.Serial}
	if m.Deliverable != "" {
		data["deliverable"] = m.Deliverable
	}
	evs := []Event{{Type: "message." + MsgStateDone, Agent: m.To, To: m.From, Data: data}}
	s.finish(&evs, now)
	return Result{"ok": true, "state": MsgStateDone}, evs, nil
}

// ObligationWindow is how long after approval a request counts as owed work.
// Approval has always existed and "done" has not, so without a bound every
// request approved before done shipped would read as owed forever.
const ObligationWindow = 24 * time.Hour

// Owed reports whether this message is a request its recipient approved and
// has not reported done, inside the window: work the recipient said it would
// do. A request that carried its own effect (a grant or an adoption) owes
// nothing, since approving it performed it.
func (m *Message) Owed(now time.Time) bool {
	if m.QueueDebt {
		return m.Type == MsgRequest && (m.State == MsgStateQueued || m.State == MsgStateApproved) && m.Grant == "" && m.Adopt == "" && m.From != m.To
	}
	return m.Type == MsgRequest && m.State == MsgStateApproved && m.Grant == "" && m.Adopt == "" &&
		m.From != m.To && now.Sub(m.TerminalAt) <= ObligationWindow
}
