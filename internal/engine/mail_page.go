// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

const (
	mailPageLimit = core.MaxMailboxPage
	mailPageBytes = 4096
)

// The cutoff fixes delivery priority for this traversal. Reading a page moves
// pending FYIs to delivered; they must not then move behind the cursor and
// appear twice. New arrivals above the cutoff belong to a new traversal.
type mailCursor struct {
	Cut     uint64 `json:"cut"`
	Rank    int    `json:"rank"`
	Serial  uint64 `json:"serial"`
	Agent   string `json:"agent"`
	Created uint64 `json:"created"`
}

type mailPage struct {
	mail []*core.Message
	next string
	more int
}

func badMailPage() error {
	return &core.Error{
		Code: "E_BAD_ARG", Msg: "invalid mailbox page",
		Hint: "call inbox without cursor to restart; limit must be between 1 and 8",
	}
}

func mailboxRank(m *core.Message, cut uint64, now time.Time) int {
	if m.Owed(now) || m.Expecting() && !m.Terminal() {
		return 0
	}
	if m.State == core.MsgStatePending || m.DeliveredAt > cut {
		return 1
	}
	if m.Type != core.MsgNotify {
		return 2
	}
	return 3
}

func (e *Engine) mailboxPage(l *core.Agent, cursor string, limit int, now time.Time) (mailPage, error) {
	if limit == 0 {
		limit = mailPageLimit
	}
	if limit < 1 || limit > mailPageLimit {
		return mailPage{}, badMailPage()
	}
	c, err := e.parseMailCursor(l, cursor)
	if err != nil {
		return mailPage{}, err
	}
	mail := e.mailboxItems(l, c.Cut, now)
	p := mailPage{}
	used := 0
	for _, m := range mail {
		rank := mailboxRank(m, c.Cut, now)
		if m.Serial > c.Cut || rank < c.Rank || rank == c.Rank && m.Serial <= c.Serial {
			continue
		}
		size, err := e.compactMailSize(m, len(p.mail) == 0)
		if err != nil {
			return mailPage{}, err
		}
		if p.more > 0 || len(p.mail) == limit || used+size > mailPageBytes {
			p.more++
			continue
		}
		p.mail = append(p.mail, m)
		used += size
	}
	return encodeMailPage(p, c, now)
}

func (e *Engine) compactMailSize(m *core.Message, first bool) (int, error) {
	raw, err := json.Marshal(e.compactMail(m))
	if err != nil {
		return 0, err
	}
	if first && len(raw) > mailPageBytes {
		return 0, &core.Error{
			Code: "E_TOO_LARGE", Msg: "one retained envelope exceeds the compact page budget",
			Hint: fmt.Sprintf("read_mail(msg_serial:%d) reads that envelope explicitly", m.Serial),
		}
	}
	return len(raw), nil
}

func encodeMailPage(p mailPage, c mailCursor, now time.Time) (mailPage, error) {
	if p.more == 0 || len(p.mail) == 0 {
		return p, nil
	}
	last := p.mail[len(p.mail)-1]
	c.Rank, c.Serial = mailboxRank(last, c.Cut, now), last.Serial
	raw, err := json.Marshal(c)
	if err != nil {
		return mailPage{}, err
	}
	p.next = base64.RawURLEncoding.EncodeToString(raw)
	return p, nil
}

// Keep the per-item receipts. Full bodies, attachments and progress history
// belong to read_mail; repeating them under both mailbox aliases broke Claude's
// result limit while the daemon nevertheless stamped delivery.
func (e *Engine) compactMail(m *core.Message) core.Result {
	out := core.Result{
		"serial": m.Serial, "from": m.From, "from_name": e.agentName(m.From),
		"to": m.To, "to_name": e.agentName(m.To), "type": m.Type, "state": m.State, "consumed": m.Consumed,
		"body":      trimRunes(strings.Join(strings.Fields(m.Body), " "), 200),
		"read_hint": fmt.Sprintf("read_mail(%d) has the full envelope", m.Serial),
	}
	if out["body"] != m.Body {
		out["body_truncated"] = true
	}
	for k, v := range map[string]uint64{
		"delivered_serial": m.DeliveredAt, "acked_serial": m.AckedAt, "responded_serial": m.RespondedAt,
		"outcome_read_serial": m.OutcomeReadAt, "review_read_serial": m.ReviewReadAt,
	} {
		if v != 0 {
			out[k] = v
		}
	}
	for k, v := range map[string]time.Time{
		"sent_at": m.SentAt, "delivered_at": m.DeliveredTime,
		"deadline": m.Deadline, "never_delivered_at": m.NeverDeliveredAt,
	} {
		if !v.IsZero() {
			out[k] = v
		}
	}
	if m.ResponseWindowSec != 0 {
		out["response_window_s"] = m.ResponseWindowSec
	}
	if m.RequestPriority != "" {
		out["request_priority"] = m.RequestPriority
	}
	return out
}

func (e *Engine) putMailboxPage(res core.Result, l *core.Agent, p mailPage) {
	mail := []core.Result{}
	for _, m := range p.mail {
		mail = append(mail, e.compactMail(m))
	}
	res["inbox"], res["messages"] = mail, mail
	res["mail_counts"] = e.mailCounts(l.ID)
	res["more_messages"] = p.more
	e.compactMailboxExtras(res, p)
	if p.next != "" {
		res["next_cursor"] = p.next
		res["mail_hint"] = fmt.Sprintf("%d more; call inbox(cursor:%q) for the next page; "+
			"read_mail(serial) has full bodies", p.more, p.next)
	}
}

// InboxPage is the agent-facing bounded read. Only the selected pending
// envelopes are ledgered as delivered; omitted items keep their wake receipts.
func (e *Engine) InboxPage(ctx context.Context, token, cursor string, limit int) (core.Result, error) {
	return e.query(ctx, func() core.Result {
		now := time.Now()
		l, refused := e.authRead(token, now)
		if refused != nil {
			return refused
		}
		p, err := e.mailboxPage(l, cursor, limit, now)
		if err != nil {
			return core.Result{"error": err}
		}
		var serials []uint64
		for _, m := range p.mail {
			if m.State == core.MsgStatePending {
				serials = append(serials, m.Serial)
			}
		}
		if len(serials) > 0 {
			if _, err = e.applyAndLedger(&core.Op{Kind: core.OpMarkDelivered, MsgSerials: serials}, now); err != nil {
				return core.Result{"error": err}
			}
		}
		res := core.Result{
			"serial": e.state.Serial, "truncated_before_serial": l.TruncatedBefore,
			"announcements": e.state.UnackedFor(l.ID), "task_queue": e.taskQueueView(l.ID),
			"owes": e.owedSerials(l.ID, now), "owed_work": e.owedWorkView(l.ID, now),
		}
		e.putMailboxPage(res, l, p)
		if gone := e.state.UnanswerableSenders(p.mail); len(gone) > 0 {
			res["unanswerable_senders"] = gone
		}
		updates, err := e.mailboxUpdates(l, now)
		if err != nil {
			return core.Result{"error": err}
		}
		if len(updates) > 0 {
			res["agent_updates"] = updates
		}
		res["serial"] = e.state.Serial
		return res
	})
}

// AckSeenFYIs acknowledges an explicit batch, never as a side effect of reading.
// Reuse the ordinary ack op and its receipts. The selection is made once on
// the writer, so a newly arriving unseen FYI cannot enter this batch.
func (e *Engine) AckSeenFYIs(ctx context.Context, token string) (core.Result, error) {
	return e.query(ctx, func() core.Result {
		now := time.Now()
		l, refused := e.authRead(token, now)
		if refused != nil {
			return refused
		}
		var ids []uint64
		for _, m := range e.state.Inbox(l.ID) {
			if m.Type == core.MsgNotify && e.notifyPresented(l.ID, m) && !m.Consumed {
				ids = append(ids, m.Serial)
			}
		}
		for _, id := range ids {
			op := &core.Op{Kind: core.OpAckMessage, Token: token, MsgSerial: id, V7Semantics: true}
			if err := e.state.Admit(op); err != nil {
				return core.Result{"error": err}
			}
			if _, err := e.applyAndLedger(op, now); err != nil {
				return core.Result{"error": err}
			}
		}
		res := core.Result{"ok": true, "acked_count": len(ids), "serial": e.state.Serial}
		if waiting := e.waiting(l.ID, now); waiting != "" {
			res["waiting"] = waiting
		}
		return res
	})
}

func (e *Engine) mailCounts(agent string) core.Result {
	fresh, seen, owed, other := 0, 0, 0, 0
	for _, m := range e.state.Inbox(agent) {
		if m.State != core.MsgStatePending && m.State != core.MsgStateDelivered {
			continue
		}
		switch {
		case m.Type == core.MsgNotify && e.notifyPresented(agent, m):
			seen++
		case !e.messagePresented(agent, m):
			fresh++
		default:
			other++
		}
	}
	for _, m := range e.state.Messages {
		if e.mailBelongsTo(m, e.state.Agents[agent]) && m.Owed(time.Now()) {
			owed++
		}
	}
	return core.Result{
		"new": fresh, "seen_unacknowledged_fyis": seen, "owed_requests": owed, "seen_awaiting_action": other,
	}
}

func (e *Engine) mailBelongsTo(m *core.Message, l *core.Agent) bool {
	return l != nil && m.To == l.ID && (m.Serial >= l.CreatedSerial || e.state.AdoptedFor(m, l.ID))
}
