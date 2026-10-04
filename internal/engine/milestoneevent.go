package engine

import (
	"fmt"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Progress/review events are derived notices, not separate mailbox messages.
// Resolve through the retained parent's entries, never a public event-ring
// lookup: strangers and replacement identities must learn no parent serial.
func (e *Engine) handleMilestoneEvent(op *core.Op, actor *core.Agent, now time.Time) (core.Result, error, bool) {
	if actor == nil || (op.Kind != core.OpAckMessage && op.Kind != core.OpRespond) {
		return nil, nil, false
	}
	m, p := e.milestoneEventFor(actor, op.MsgSerial)
	if m == nil {
		return nil, nil, false
	}
	if op.Kind == core.OpRespond {
		hint := fmt.Sprintf("this is an event, not the request: read_mail(msg_serial:%d), "+
			"or ack(msg_serial:%d) to dismiss it", m.Serial, p.Serial)
		if p.Review == "" && p.Milestone > 0 {
			hint += fmt.Sprintf("; review with respond(msg_serial:%d, disposition:\"accept\"|\"flag\", "+
				"milestone:%d), including body for flag", m.Serial, p.Milestone)
		}
		return nil, &core.Error{
			Code: "E_WRONG_KIND", Msg: "respond needs the request serial, not a milestone event", Hint: hint,
		}, true
	}
	alsoRead, err := e.acknowledgeOutcomeEvent(actor.ID, op.Token, m, p, now)
	if err != nil {
		return nil, err, true
	}
	e.dismissMilestoneNotice(actor.ID, p.Serial)
	e.seen[actor.ID] = now
	e.noteAuthenticatedContact(actor, now)
	e.confirmSocketOffer(actor, now)
	result := core.Result{"ok": true, "state": "acked"}
	if len(alsoRead) > 0 {
		result["also_read"] = alsoRead
	}
	return result, nil, true
}

func (e *Engine) acknowledgeOutcomeEvent(
	agent, token string, m *core.Message, p core.Progress, now time.Time,
) ([]uint64, error) {
	var alsoRead []uint64
	units := e.outcomeUnits(m)
	if m.From != agent {
		units = e.reviewUnits(m)
	}
	for _, older := range units {
		if older.serial < p.Serial {
			alsoRead = append(alsoRead, older.serial)
		}
	}
	_, err := e.applyAndLedger(&core.Op{
		Kind: core.OpOutcomeRead, Token: token,
		MsgSerial: m.Serial, OutcomeThroughSerial: p.Serial,
	}, now)
	return alsoRead, err
}

func (e *Engine) dismissMilestoneNotice(agent string, serial uint64) {
	kept := e.notices[agent][:0]
	for _, n := range e.notices[agent] {
		if n.Serial != serial {
			kept = append(kept, n)
		}
	}
	if len(kept) == 0 {
		delete(e.notices, agent)
	} else {
		e.notices[agent] = kept
	}
}

func (e *Engine) milestoneEventFor(actor *core.Agent, serial uint64) (*core.Message, core.Progress) {
	for _, m := range e.state.Messages {
		adopted := m.To == actor.ID && e.state.AdoptedFor(m, actor.ID)
		if actor.CreatedSerial > 0 && m.Serial < actor.CreatedSerial && !adopted {
			continue
		}
		for _, p := range m.Progress {
			if p.Serial != serial || (p.Review == "" && m.From != actor.ID) || (p.Review != "" && m.To != actor.ID) {
				continue
			}
			return m, p
		}
	}
	return nil, core.Progress{}
}
