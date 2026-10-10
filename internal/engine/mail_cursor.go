// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"encoding/base64"
	"encoding/json"
	"sort"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func (e *Engine) parseMailCursor(l *core.Agent, cursor string) (mailCursor, error) {
	c := mailCursor{Cut: e.state.Serial, Rank: -1, Agent: l.ID, Created: l.CreatedSerial}
	if cursor == "" {
		return c, nil
	}
	if len(cursor) > 1024 {
		return c, badMailPage()
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || json.Unmarshal(raw, &c) != nil {
		return c, badMailPage()
	}
	if c.Agent != l.ID || c.Created != l.CreatedSerial || c.Cut > e.state.Serial ||
		c.Rank < 0 || c.Rank > 3 || c.Serial == 0 || c.Serial > c.Cut {
		return c, badMailPage()
	}
	return c, nil
}

func (e *Engine) mailboxItems(l *core.Agent, cut uint64, now time.Time) []*core.Message {
	mail := e.state.Inbox(l.ID)
	included := map[uint64]bool{}
	for _, m := range mail {
		included[m.Serial] = true
	}
	for _, m := range e.state.Messages {
		if e.mailBelongsTo(m, l) && m.Owed(now) && !included[m.Serial] {
			mail = append(mail, m)
		}
	}
	sort.Slice(mail, func(i, j int) bool {
		a, b := mailboxRank(mail[i], cut, now), mailboxRank(mail[j], cut, now)
		if a != b {
			return a < b
		}
		return mail[i].Serial < mail[j].Serial
	})
	return mail
}
