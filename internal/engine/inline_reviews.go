package engine

import (
	"fmt"
	"sort"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func (e *Engine) reviewUnits(m *core.Message) []outcomeUnit {
	if m.State != core.MsgStateApproved && m.State != core.MsgStateDone {
		return nil
	}
	if e.state.ReviewReadCutoff == 0 || !m.RetainUntil.After(time.Now()) {
		return nil // no upgrade flood and no resurrection past retention
	}
	through := max(m.ReviewReadAt, e.state.ReviewReadCutoff)
	var units []outcomeUnit
	for _, p := range m.Progress {
		if p.Review == "" || p.Serial <= through {
			continue
		}
		verb := "accepted"
		if p.Review == core.ReviewFlagged {
			verb = "FLAGGED"
		}
		text := fmt.Sprintf("%s %s the work on your request (msg %d)", p.By, verb, m.Serial)
		if p.Milestone > 0 && p.Milestone <= len(m.Milestones) {
			text += fmt.Sprintf(": milestone %d of %d, %q", p.Milestone, len(m.Milestones), m.Milestones[p.Milestone-1])
		}
		units = append(units, outcomeUnit{
			serial: p.Serial, text: text, body: p.Note,
			at: p.At, blocking: p.Review == core.ReviewFlagged,
		})
	}
	return units
}

func (e *Engine) receivedReviews(agent string) []*core.Message {
	if e.state == nil {
		return nil
	}
	l := e.state.Agents[agent]
	if l == nil || l.Retired() {
		return nil
	}
	var messages []*core.Message
	for _, m := range e.state.Messages {
		if m.To == agent && m.From != agent &&
			(l.CreatedSerial == 0 || m.Serial >= l.CreatedSerial || e.state.AdoptedFor(m, agent)) && len(e.reviewUnits(m)) > 0 {
			messages = append(messages, m)
		}
	}
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].LatestReviewSerial() > messages[j].LatestReviewSerial()
	})
	return messages
}

func noticeIsReview(m *core.Message, serial uint64) bool {
	for _, p := range m.Progress {
		if p.Serial == serial && p.Review != "" {
			return true
		}
	}
	return false
}
