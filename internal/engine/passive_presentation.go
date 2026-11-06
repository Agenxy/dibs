// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/agenxy/dibs/internal/core"
)

// Presentation is not mail state. Losing this writer-owned cache on restart
// repeats a legitimate pointer; it cannot consume mail or spend a wake. Keep
// only the current session for each agent, including its incarnation, so a move
// or a recreated agent always gets its own first presentation.
type passivePresentation struct {
	session string
	created uint64
	state   [sha256.Size]byte
}

// Fingerprint identities and states, never rendered text, counts or the global
// serial. The bounded digest can stay byte-identical when a hidden item changes.
// Item serials identify immutable content; outcome/review units have their own
// immutable revision serials. Nothing here depends on elapsed wall-clock time.
func (e *Engine) passivePresentationFor(l *core.Agent) passivePresentation {
	var b strings.Builder
	for _, m := range e.state.Inbox(l.ID) {
		if !e.mailBelongsTo(m, l) || m.Terminal() {
			continue
		}
		fmt.Fprintf(&b, "mail:%d:%s:%t:%t\n", m.Serial, m.State,
			e.messagePresented(l.ID, m), e.notifyPresented(l.ID, m))
	}
	for _, g := range e.outcomeGroups(l.ID) {
		for _, u := range g.units {
			fmt.Fprintf(&b, "update:%d:%d:%s\n", g.message.Serial, u.serial, u.kind)
		}
	}
	for _, n := range e.takeNotices(l.ID) {
		if !n.ReadParent {
			fmt.Fprintf(&b, "notice:%d:%d:%s:%t:%q\n", n.Serial, n.Msg, n.Kind, n.Delivered, n.Text)
		}
	}
	for _, a := range e.state.Unacked(l.ID) {
		fmt.Fprintf(&b, "announcement:%d:%s\n", a.Serial, a.State)
	}
	return passivePresentation{
		session: socketSessionKey(l), created: l.CreatedSerial,
		state: sha256.Sum256([]byte(b.String())),
	}
}

func (e *Engine) prunePassivePresentations() {
	for id, p := range e.passivePresented {
		l := e.state.Agents[id]
		if l == nil || l.Retired() || l.CreatedSerial != p.created || socketSessionKey(l) != p.session {
			delete(e.passivePresented, id)
		}
	}
}
