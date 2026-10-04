package engine

import (
	"time"

	"github.com/agenxy/dibs/internal/core"
)

type contactEvidence struct {
	at      time.Time
	created uint64
}

// Successful token-authenticated model calls prove the identity is present,
// even when its old harness PID is gone. Subscription machinery uses
// authObserve and does not enter here. The seen stamps in boot and in exec's
// register/resume result path deliberately do not create authenticated contact.
func (e *Engine) noteAuthenticatedContact(l *core.Agent, now time.Time) {
	if e.contact == nil {
		e.contact = map[string]contactEvidence{}
	}
	e.contact[l.ID] = contactEvidence{at: now, created: l.CreatedSerial}
	e.noteSocketBusy(l, now)
}

// Use the existing idle lease, not a new clock. Once direct contact expires,
// the old PID can again establish a crash. The decision is recorded in the
// normal sweep op; the fold never consults this ephemeral evidence or a PID.
func (e *Engine) recentAuthenticatedContact(l *core.Agent, now time.Time) bool {
	c, ok := e.contact[l.ID]
	return ok && c.created == l.CreatedSerial && now.Sub(c.at) <= e.state.Limits.IdleTTL
}

// Bound the derived cache to live incarnations and the same freshness window.
func (e *Engine) trimContactEvidence(now time.Time) {
	for id, c := range e.contact {
		l := e.state.Agents[id]
		if l == nil || l.Status != core.StatusActive || l.CreatedSerial != c.created ||
			now.Sub(c.at) > e.state.Limits.IdleTTL {
			delete(e.contact, id)
		}
	}
}
