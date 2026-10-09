// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import "slices"

// Only authenticated pulls call this, after their updates were rendered.
// Reading reachability news consumes that notice, not the source mail or the
// contact's independent human delivery. Other generic notices keep their rules.
func (e *Engine) consumeContactNotices(agent string, shown map[uint64]bool) {
	kept := slices.DeleteFunc(e.notices[agent], func(n notice) bool {
		return n.Kind == "contact.escalated" && shown[n.Serial]
	})
	if len(kept) == 0 {
		delete(e.notices, agent)
	} else {
		e.notices[agent] = kept
	}
}
