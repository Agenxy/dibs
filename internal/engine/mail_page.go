// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
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

// mailboxRank orders a page: mail the agent has not been shown, then work it
// owes and asks it has seen but not answered, then other retained mail, then
// FYIs. Oldest first within a rank, so nothing in a rank is pushed back for
// ever.
//
// Two failures bound this. Owed work once ranked above new mail with eight
// items to a page, so an agent with eight open requests never saw new mail on
// page one: k7-dev's notifies from 2026-10-06 sat undelivered for eight days.
// Merging the two into one rank, oldest first, fixed that only while owed
// lists were short: in the 2026-10-18 fleet survey seven agents reported a new
// question on page three behind ten to thirty-five parked, approved requests,
// some thirteen days old, with three inbox calls per wake to find it. Work an
// agent owes is never lost by ranking it second: every check_in and inbox
// carries the owed_work index with each item's completion call, and the page
// itself reaches it next. What an agent has not yet been shown has no other
// place to be seen.
//
// An FYI a wake only ANNOUNCED (a pointer, not its content) is still unseen,
// so it ranks ahead of owed work too, behind mail nobody has pointed at yet:
// codex-k7-0 followed two cursors past eleven owed requests to reach a
// broadcast its wake had announced (2026-10-18, 189240). It stays below fresh
// mail because announced FYIs can pile up by the hundred and must not bury a
// new question.
//
// "Not yet shown" is fixed at the traversal's cutoff: reading page one marks
// its mail delivered, and an item must not change rank mid-traversal.
// maxMailRank is mailboxRank's last rank, which a cursor may carry. A literal
// in the cursor parser silently refused every cursor past page one the day a
// rank was added.
const maxMailRank = 4

func mailboxRank(m *core.Message, cut uint64, now time.Time) int {
	unseen := m.State == core.MsgStatePending || m.DeliveredAt > cut
	announcedOnly := m.Type == core.MsgNotify &&
		m.NotifyAnnouncedAt >= max(m.AdoptedAt, 1) && m.NotifyAnnouncedAt <= cut
	switch {
	case unseen && !announcedOnly:
		return 0
	case unseen:
		return 1
	case m.Owed(now) || m.Expecting() && !m.Terminal():
		return 2
	case m.Type != core.MsgNotify:
		return 3
	}
	return maxMailRank
}

// lateAfter is how long mail may wait before a page says it is late.
const lateAfter = time.Hour

// lateNote says, on mail being shown for the first time long after it was
// sent, how long it waited: stale content read as fresh costs a triage pass.
func lateNote(m *core.Message, now time.Time) string {
	shown := now
	if !m.DeliveredTime.IsZero() {
		shown = m.DeliveredTime
	}
	if m.SentAt.IsZero() || shown.Sub(m.SentAt) < lateAfter {
		return ""
	}
	return "delivered " + roughAge(shown.Sub(m.SentAt)) + " after it was sent; it may be out of date"
}

func roughAge(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return strconv.Itoa(int(d/(24*time.Hour))) + " days"
	case d >= 2*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + " hours"
	}
	return strconv.Itoa(int(d/time.Minute)) + " minutes"
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
		size, err := e.compactMailSize(m, len(p.mail) == 0, now)
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

func (e *Engine) compactMailSize(m *core.Message, first bool, now time.Time) (int, error) {
	raw, err := json.Marshal(e.compactMail(m, now))
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
func (e *Engine) compactMail(m *core.Message, now time.Time) core.Result {
	out := core.Result{
		"serial": m.Serial, "from": m.From, "from_name": e.agentName(m.From),
		"to": m.To, "to_name": e.agentName(m.To), "type": m.Type, "state": m.State, "consumed": m.Consumed,
		"body": trimRunes(strings.Join(strings.Fields(m.Body), " "), 200),
		"read_hint": fmt.Sprintf("read_mail(%d) reads this conversation; include_body:true returns the full envelope",
			m.Serial),
	}
	if out["body"] != m.Body {
		out["body_truncated"] = true
	}
	if m.Type == core.MsgNotify {
		short := out["body"]
		out["body"] = m.Body
		raw, err := json.Marshal(out)
		if err == nil && len(raw) <= mailPageBytes-1024 {
			delete(out, "body_truncated")
		} else {
			out["body"] = short
		}
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
	if note := lateNote(m, now); note != "" {
		out["late"] = note
	}
	if m.RequestPriority != "" {
		out["request_priority"] = m.RequestPriority
	}
	return out
}

func (e *Engine) putMailboxPage(res core.Result, l *core.Agent, p mailPage) {
	e.refreshMailboxReceipts(res, l, p)
	res["more_messages"] = p.more
	e.compactMailboxExtras(res, p)
	if p.next != "" {
		res["next_cursor"] = p.next
		res["mail_hint"] = fmt.Sprintf("%d more; call inbox(cursor:%q) for the next page; "+
			"read_mail(serial, include_body:true) has full bodies", p.more, p.next)
	}
}

// Refresh post-presentation receipts without filtering auxiliary projections
// a second time: doing that erases their already-computed omitted counts.
func (e *Engine) refreshMailboxReceipts(res core.Result, l *core.Agent, p mailPage) {
	mail := []core.Result{}
	for _, m := range p.mail {
		mail = append(mail, e.compactMail(m, time.Now()))
	}
	res["inbox"], res["messages"] = mail, mail
	res["mail_counts"] = e.mailCounts(l.ID)
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
		if err := e.deliverMailboxPage(p, now); err != nil {
			return core.Result{"error": err}
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
		return e.completeInboxPage(res, l, p, now)
	})
}

func (e *Engine) deliverMailboxPage(p mailPage, now time.Time) error {
	var serials []uint64
	for _, m := range p.mail {
		if m.State == core.MsgStatePending {
			serials = append(serials, m.Serial)
		}
	}
	if len(serials) == 0 {
		return nil
	}
	_, err := e.applyAndLedger(&core.Op{Kind: core.OpMarkDelivered, MsgSerials: serials}, now)
	return err
}

func (e *Engine) completeInboxPage(res core.Result, l *core.Agent, p mailPage, now time.Time) core.Result {
	updates, err := e.mailboxUpdates(l, now)
	if err != nil {
		return core.Result{"error": err}
	}
	if len(updates) > 0 {
		res["agent_updates"] = updates
	}
	if err := e.presentMailboxFYIs(res, l, p, now); err != nil {
		return core.Result{"error": err}
	}
	e.refreshMailboxReceipts(res, l, p)
	res["serial"] = e.state.Serial
	return res
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
		"new": fresh, "announced_fyis": seen, "owed_requests": owed, "seen_awaiting_action": other,
	}
}

func (e *Engine) mailBelongsTo(m *core.Message, l *core.Agent) bool {
	return l != nil && m.To == l.ID && (m.Serial >= l.CreatedSerial || e.state.AdoptedFor(m, l.ID))
}
