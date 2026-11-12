// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"encoding/json"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Conservative result cap: complete FYI bodies are spent only inside a small
// result. A full read whose retained history exceeds this cap remains readable
// without pretending a harness's oversized-result file pointer presented it.
const fyiResultBytes = 16384

type fyiPresentation struct {
	serial uint64
	full   bool
}

func (e *Engine) announceFYI(l *core.Agent, id uint64, now time.Time) error {
	m := e.state.Messages[id]
	if m == nil || m.Consumed || m.NotifyAnnouncedAt >= max(m.AdoptedAt, 1) {
		return nil
	}
	op := &core.Op{Kind: core.OpActivityCheckpoint, Token: l.Token, NotifyAnnounced: []uint64{id}, V7Semantics: true}
	if err := e.state.Admit(op); err != nil {
		return err
	}
	_, err := e.applyAndLedger(op, now)
	return err
}

func (e *Engine) consumeFYI(l *core.Agent, id uint64, reason string, now time.Time) error {
	m := e.state.Messages[id]
	if m == nil || m.Consumed {
		return nil
	}
	op := &core.Op{Kind: core.OpAckMessage, Token: l.Token, MsgSerial: id, NotifyConsumption: reason, V7Semantics: true}
	if err := e.state.Admit(op); err != nil {
		return err
	}
	_, err := e.applyAndLedger(op, now)
	return err
}

func (e *Engine) recordFYIPresentation(l *core.Agent, shown []fyiPresentation, reminder bool, now time.Time) error {
	for _, item := range shown {
		m := e.state.Messages[item.serial]
		if m == nil || !e.mailBelongsTo(m, l) || m.Consumed {
			continue
		}
		if err := e.recordMailPresentation(l, m, item.full, reminder, now); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) recordMailPresentation(l *core.Agent, m *core.Message, full, reminder bool, now time.Time) error {
	switch m.Type {
	case core.MsgHandoff:
		// Handoffs still need an explicit ack. Complete presentation records
		// the same replayable delivery as read_mail, without consuming it.
		// Pointer-only hooks and unconfirmed offers cannot manufacture one.
		if full && m.State == core.MsgStatePending {
			op := &core.Op{Kind: core.OpMarkDelivered, MsgSerials: []uint64{m.Serial}}
			_, err := e.applyAndLedger(op, now)
			return err
		}
	case core.MsgNotify:
		if full {
			return e.consumeFYI(l, m.Serial, "presented", now)
		}
		if reminder && e.notifyPresented(l.ID, m) {
			return e.consumeFYI(l, m.Serial, "reminded", now)
		}
		return e.announceFYI(l, m.Serial, now)
	}
	return nil
}

func resultFitsFYIPresentation(res core.Result) bool {
	raw, err := json.Marshal(res)
	return err == nil && len(raw) <= fyiResultBytes
}

func (e *Engine) recordHookFYIs(
	l *core.Agent, event, digest string, shown []fyiPresentation, now time.Time,
) error {
	preview := core.Result{}
	addDelivery(preview, event, digest)
	if !resultFitsFYIPresentation(preview) {
		return nil
	}
	return e.recordFYIPresentation(l, shown, false, now)
}

func (e *Engine) presentMailboxFYIs(res core.Result, l *core.Agent, p mailPage, now time.Time) error {
	if !resultFitsFYIPresentation(res) {
		return nil
	}
	var shown []fyiPresentation
	for _, m := range p.mail {
		item := e.compactMail(m, now)
		shown = append(shown, fyiPresentation{serial: m.Serial, full: item["body_truncated"] != true})
	}
	return e.recordFYIPresentation(l, shown, false, now)
}
