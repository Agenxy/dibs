// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Command acceptance is per original item, never a successful-delivery timer.
// Capture before execution; mail that arrives while it runs stays unoffered.
// This derived receipt consumes neither mailbox state nor Stop presentation.
func (e *Engine) freshCommandKeys(agent string, now time.Time) []string {
	var keys []string
	for _, key := range e.deliveryKeysAt(agent, now) {
		if !e.commandWritten[key] {
			keys = append(keys, key)
		}
	}
	return keys
}

func (e *Engine) recordCommandWritten(cmd wakePlan) {
	if len(cmd.commandKeys) == 0 {
		return
	}
	_, _ = e.query(e.wakeContext, func() core.Result {
		l := e.state.Agents[cmd.agent]
		if l == nil || l.CreatedSerial != cmd.createdSerial || !l.SessionIsCurrent(cmd.session) {
			return nil
		}
		if e.commandWritten == nil {
			e.commandWritten = map[string]bool{}
		}
		for _, key := range cmd.commandKeys {
			e.commandWritten[key] = true
		}
		return nil
	})
}
