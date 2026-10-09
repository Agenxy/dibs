// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import "github.com/agenxy/dibs/internal/core"

func (e *Engine) releaseSocketOffer(id string, written bool) {
	for who, other := range e.socketOffers {
		if other.id != id {
			continue
		}
		if !written {
			if e.socketFailedOffers == nil {
				e.socketFailedOffers = map[string]socketOffer{}
			}
			e.socketFailedOffers[who] = other
		}
		delete(e.socketOffers, who)
	}
}

func (e *Engine) failedOfferOutstanding(agents []*core.Agent, session, id string) bool {
	owed := false
	for _, l := range agents {
		old := e.socketFailedOffers[l.ID]
		if old.id != id || old.session != session {
			continue
		}
		var keys []string
		for _, k := range old.mail {
			keys = append(keys, "mail:"+k)
		}
		for _, k := range old.notices {
			keys = append(keys, "notice:"+k)
		}
		for _, k := range old.announcements {
			keys = append(keys, "announcement:"+k)
		}
		owed = owed || e.failedCauseOutstanding(l.ID, keys)
		delete(e.socketFailedOffers, l.ID)
	}
	return owed
}
