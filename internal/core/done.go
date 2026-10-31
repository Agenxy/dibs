// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

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
//
// done on a request nobody approved yet approves it in the same op. Nine agents
// in one fleet survey (2026-10-18) reported the same three refused guesses
// before finding approve-then-done: "answer", then "done", then "approve". The
// approval carries no information the delivery does not, so requiring it as a
// separate call was a round trip with nothing in it. Both events are emitted,
// so the requester sees what happened in the order it happened. Narrowed to a
// plain request still waiting for its first verdict: a queued one has a queue
// position to give up, and a grant or adoption is performed by approving it, so
// both keep their explicit approve. Every earlier build refused done on an
// unapproved request, so no ledger holds one and replay is unchanged.
//
// A milestone given with done is reported as reached as the request closes,
// which is what an agent finishing the last step means by it.
func (s *State) applyDone(m *Message, op *Op, now time.Time) (Result, []Event, error) {
	impliedApproval := m.approvableByDone()
	if err := s.refuseDone(m, op, impliedApproval); err != nil {
		return nil, nil, err
	}
	var evs []Event
	if impliedApproval {
		m.Consumed = true // responding proves receipt (SPEC §8)
		evs = append(evs, Event{
			Type: "message." + MsgStateApproved, Agent: m.To, To: m.From,
			Data: map[string]any{"msg_serial": m.Serial, "with": MsgStateDone},
		})
	}
	data := map[string]any{"msg_serial": m.Serial}
	if op.Milestone > 0 {
		m.Progress = append(m.Progress, Progress{
			Milestone: op.Milestone, Artifact: op.Deliverable, By: m.To, Serial: s.Serial + 1, At: now,
		})
		data["milestone"], data["label"] = op.Milestone, m.Milestones[op.Milestone-1]
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
	if m.Deliverable != "" {
		data["deliverable"] = m.Deliverable
	}
	evs = append(evs, Event{Type: "message." + MsgStateDone, Agent: m.To, To: m.From, Data: data})
	s.finish(&evs, now)
	return Result{"ok": true, "state": MsgStateDone}, evs, nil
}

// refuseDone holds done's state rules, in the fold because they read the
// message.
func (s *State) refuseDone(m *Message, op *Op, impliedApproval bool) error {
	if m.Type != MsgRequest || (m.State != MsgStateApproved && !impliedApproval) {
		state := m.State
		if state == "" {
			state = "pending"
		}
		hint := questionResponseHint(m, "done closes a request once the work is delivered; a queued "+
			"request needs approve first, and a grant, adoption or merge is performed by approving it")
		return errf("E_BAD_DISPOSITION", hint,
			"message %d is a %s and %s, not an approved request", m.Serial, m.Type, state)
	}
	if m.Grant != "" || m.Adopt != "" && (!op.AdoptionRecovery || m.PriorAdoptionSerial == 0) || m.RetiresIdentity() {
		return errf("E_BAD_DISPOSITION",
			"approving it already performed it: there is no work left to report",
			"message %d carried its own effect", m.Serial)
	}
	if len(op.Body) > s.Limits.MaxBodyBytes {
		return errTooLarge("response body", s.Limits.MaxBodyBytes)
	}
	if op.Milestone > len(m.Milestones) {
		hint := "this request named no milestones: close it with done and no milestone"
		if len(m.Milestones) > 0 {
			hint = "its milestones are numbered 1 to " + itoa(len(m.Milestones)) + "; read_mail lists them"
		}
		return errf("E_BAD_ARG", hint, "message %d has no milestone %d", m.Serial, op.Milestone)
	}
	if op.Milestone > 0 && len(m.Progress) >= maxProgressReports {
		return errf("E_QUOTA", "close it with done and no milestone: this request holds "+
			"all the progress reports it can", "message %d has %d reports", m.Serial, len(m.Progress))
	}
	return nil
}

// approvableByDone: a plain request still waiting for its first verdict.
func (m *Message) approvableByDone() bool {
	return m.Type == MsgRequest && m.Grant == "" && m.Adopt == "" && !m.RetiresIdentity() && !m.Terminal()
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
	if m.DurableDebt() {
		return true
	}
	return m.Type == MsgRequest && m.State == MsgStateApproved && m.Grant == "" && m.Adopt == "" && !m.RetiresIdentity() &&
		m.From != m.To && now.Sub(m.TerminalAt) <= ObligationWindow
}

// DurableDebt identifies explicit post-queue acceptance without applying a clock
// to historical approvals. It is work to recover even when its verdict is consumed.
func (m *Message) DurableDebt() bool {
	return m.QueueDebt && m.Type == MsgRequest && (m.State == MsgStateQueued || m.State == MsgStateApproved) &&
		m.Grant == "" && m.Adopt == "" && !m.RetiresIdentity() && m.From != m.To
}
