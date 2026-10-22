// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"slices"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// The escalation's first event serial is its contact serial. Resolution makes
// that reachability warning obsolete, independently of whether the coordinator
// read it. This is derived from replayed state, not a fabricated read receipt.
func (e *Engine) contactNoticeObsolete(n notice) bool {
	if n.Kind != "contact.escalated" || e.state == nil {
		return false
	}
	c := e.state.Contacts[n.Serial]
	return c == nil || !c.ResolvedAt.IsZero()
}

// Only authenticated pulls call this, after their updates were rendered.
// Reading reachability news consumes that notice, not the source mail or the
// contact's independent human delivery. Other generic notices keep their rules.
func (e *Engine) consumeContactNotices(agent string, shown map[uint64]bool, now time.Time) error {
	var through uint64
	for serial := range shown {
		through = max(through, serial)
	}
	l := e.state.Agents[agent]
	if l != nil && through > l.ContactNoticeReadAt {
		op := &core.Op{Kind: core.OpActivityCheckpoint, Token: l.Token, ContactNoticeThroughSerial: through}
		if err := e.state.Admit(op); err != nil {
			return err
		}
		if _, err := e.applyAndLedger(op, now); err != nil {
			return err
		}
	}
	kept := slices.DeleteFunc(e.notices[agent], func(n notice) bool {
		return n.Kind == "contact.escalated" && shown[n.Serial]
	})
	if len(kept) == 0 {
		delete(e.notices, agent)
	} else {
		e.notices[agent] = kept
	}
	return nil
}
