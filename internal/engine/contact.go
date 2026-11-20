// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"time"

	"github.com/agenxy/dibs/internal/core"
)

type contactEvidence struct {
	at      time.Time
	created uint64
}

// Authenticated contact after a process exited disproves that PID as
// evidence of closure for this activation, independently of the idle lease.
type processActivation struct {
	created, activation uint64
	pid                 int
	start               int64
}

func recordedProcess(l *core.Agent) processActivation {
	return processActivation{l.CreatedSerial, l.Activation, l.PID, l.ProcStart}
}

// Successful token-authenticated model calls prove the identity is present,
// even when its old harness PID is gone. Subscription machinery uses
// authObserve and does not enter here. The seen stamps in boot and in exec's
// register/resume result path deliberately do not create authenticated contact.
func (e *Engine) noteAuthenticatedContact(l *core.Agent, now time.Time) {
	delete(e.acceptedContacts, l.ID)
	delete(e.nativeTurnWatches, l.ID)
	if e.contact == nil {
		e.contact = map[string]contactEvidence{}
	}
	e.contact[l.ID] = contactEvidence{at: now, created: l.CreatedSerial}
	if l.PID > 1 && e.prober != nil && e.ownsHost(l) && !e.prober.Alive(l.PID) {
		if e.pastProcessExit == nil {
			e.pastProcessExit = map[string]processActivation{}
		}
		e.pastProcessExit[l.ID] = recordedProcess(l)
	}
	e.noteSocketBusy(l, now)
}

// Use the existing idle lease, not a new clock. Once direct contact expires,
// the old PID can again establish a crash. The decision is recorded in the
// normal sweep op; the fold never consults this ephemeral evidence or a PID.
func (e *Engine) recentAuthenticatedContact(l *core.Agent, now time.Time) bool {
	c, ok := e.contact[l.ID]
	return ok && c.created == l.CreatedSerial && now.Sub(c.at) <= e.state.Limits.IdleTTL
}

// Retire replaced process observations and expire idle-lease contact evidence.
func (e *Engine) trimContactEvidence(now time.Time) {
	for id, earlier := range e.pastProcessExit {
		l := e.state.Agents[id]
		if l == nil || recordedProcess(l) != earlier {
			delete(e.pastProcessExit, id)
		}
	}
	for id, c := range e.contact {
		l := e.state.Agents[id]
		if l == nil || l.Status != core.StatusActive || l.CreatedSerial != c.created ||
			now.Sub(c.at) > e.state.Limits.IdleTTL {
			delete(e.contact, id)
		}
	}
}
