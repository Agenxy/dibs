// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

type contactAttempt struct {
	count   int
	armed   bool
	failure string
}
type contactAlertView struct {
	*core.ContactEscalation
	DeliveryAttempts int    `json:"delivery_attempts,omitempty"`
	DeliveryFailure  string `json:"delivery_failure,omitempty"`
	RetryExhausted   bool   `json:"retry_exhausted,omitempty"`
}

// On the writer: only a failed post arms the single retry of this contact.
// Missing receipts and time passing are not evidence of delivery failure.
func (e *Engine) contactDeliveryFailed(serial uint64, reason string) {
	c := e.state.Contacts[serial]
	if c == nil || !c.NotifiedAt.IsZero() || !c.ResolvedAt.IsZero() {
		return
	}
	rec := e.contactAttempts[serial]
	if rec.count == 0 {
		rec.count = 1
	} // first post came through a relay reconnect
	rec.failure = reason
	if rec.failure == "" {
		rec.failure = "the notification route reported a failed post"
	}
	if rec.count < 2 && !rec.armed {
		rec.armed = true
		time.AfterFunc(contactRetryInterval, func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = e.query(ctx, func() core.Result {
				current := e.state.Contacts[serial]
				r := e.contactAttempts[serial]
				if current != nil && current.NotifiedAt.IsZero() && current.ResolvedAt.IsZero() && r.armed {
					e.startContactDelivery(serial)
				}
				return nil
			})
		})
	}
	if e.contactAttempts == nil {
		e.contactAttempts = map[uint64]contactAttempt{}
	}
	e.contactAttempts[serial] = rec
}

func (e *Engine) decorateContactAlerts(b core.Result) {
	alerts, _ := b["contact_alerts"].([]*core.ContactEscalation)
	views := make([]contactAlertView, 0, len(alerts))
	for _, c := range alerts {
		rec := e.contactAttempts[c.Serial]
		views = append(views, contactAlertView{c, rec.count, rec.failure, rec.count >= 2 && rec.failure != ""})
	}
	if len(views) > 0 {
		b["contact_alerts"] = views
	}
	for serial := range e.contactAttempts {
		c := e.state.Contacts[serial]
		if c == nil || !c.NotifiedAt.IsZero() || !c.ResolvedAt.IsZero() {
			delete(e.contactAttempts, serial)
		}
	}
}
