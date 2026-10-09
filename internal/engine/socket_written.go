// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"strconv"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Socket transport dedup must never affect the shared actionable rule used
// by Stop and reconnect delivery: a write is not receiver acceptance.
func (e *Engine) hasUnwrittenSocketCause(l *core.Agent, now time.Time) bool {
	if e.hasUnwrittenSocketAnnouncement(l.ID) {
		return true
	}
	wanted := map[string]bool{}
	for _, key := range e.unwrittenSocketKeys("mail:", e.wakeKeys(l.ID, now)) {
		wanted[key] = true
	}
	for _, m := range e.state.Inbox(l.ID) {
		if wanted[noticeKey(l.ID, m.Serial)] && e.socketActionableMessage(m) {
			return true
		}
	}
	for _, n := range e.takeNotices(l.ID) {
		if !e.socketWritten["notice:"+noticeKey(l.ID, n.Serial)] && !n.Delivered &&
			socketActionableNotice(n, l, e.state.Messages[n.Msg]) {
			return true
		}
	}
	return false
}

func (e *Engine) hasUnwrittenSocketAnnouncement(agent string) bool {
	for _, a := range e.state.Unacked(agent) {
		if !e.socketWritten["announcement:"+noticeKey(agent, a.Serial)] {
			return true
		}
	}
	return false
}

// A write receipt deduplicates bytes only. It must not spend the model's
// presentation or consume mail: held peers retain their Stop fallback.
func (e *Engine) noteSocketWritten(offer socketOffer) {
	if e.socketWritten == nil {
		e.socketWritten = map[string]bool{}
	}
	for kind, keys := range map[string][]string{
		"mail:": offer.mail, "notice:": offer.notices, "announcement:": offer.announcements,
	} {
		for _, key := range keys {
			e.socketWritten[kind+key] = true
		}
	}
}

func (e *Engine) unwrittenSocketKeys(kind string, keys []string) []string {
	var out []string
	for _, key := range keys {
		if !e.socketWritten[kind+key] {
			out = append(out, key)
		}
	}
	return out
}

// Bound the derived receipt view to retained coordination items. A restart
// loses these transport receipts, never mail or the one-writer claims.
func (e *Engine) pruneSocketWritten() {
	live := map[string]bool{}
	for id, row := range e.state.Agents {
		if row.Retired() {
			continue
		}
		for _, m := range e.state.Inbox(id) {
			live["mail:"+noticeKey(id, m.Serial)] = true
		}
		for _, n := range e.takeNotices(id) {
			live["notice:"+noticeKey(id, n.Serial)] = true
		}
		for _, group := range e.outcomeGroups(id) {
			for _, unit := range group.units {
				live["notice:"+noticeKey(id, unit.serial)] = true
			}
		}
		for _, a := range e.state.Unacked(id) {
			live["announcement:"+id+"\x00"+strconv.FormatUint(a.Serial, 10)] = true
		}
	}
	e.pruneCommandWritten(live)
	for key := range e.socketWritten {
		if !live[key] {
			delete(e.socketWritten, key)
		}
	}
}
