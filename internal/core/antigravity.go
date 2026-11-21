// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import "time"

// A return to a previously held alias needs a new observation from the app.
func (a *Agent) setCurrentSession(session string) {
	if a.CurrentSession != session {
		a.CurrentSession = session
		a.AntigravityBindingSerial = 0
	}
}

func (s *State) applyAntigravityBinding(l *Agent, conversation string, now time.Time) (Result, []Event) {
	res := Result{"ok": true, "changed": false}
	if conversation == "" {
		if l.AntigravityBindingSerial == 0 {
			return res, nil
		}
		l.AntigravityBindingSerial = 0
	} else {
		if l.CurrentSession != conversation || l.AntigravityBindingSerial != 0 {
			return res, nil
		}
		l.AntigravityBindingSerial = s.Serial + 1
	}
	res["changed"] = true
	evs := []Event{{Type: "agent.updated", Agent: l.ID}}
	s.finish(&evs, now)
	return res, evs
}
