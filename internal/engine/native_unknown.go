// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// A missing app reply proves neither success nor failure. Retain the original
// items as unknown until the receiving incarnation changes; new items qualify.
// This is derived evidence, never a ledger or mailbox mutation.
func (e *Engine) recordNativeUnknown(plan wakePlan) {
	_, _ = e.query(e.wakeContext, func() core.Result {
		l := e.state.Agents[plan.agent]
		if l == nil || l.Retired() || l.CreatedSerial != plan.createdSerial ||
			!l.SessionIsCurrent(plan.session) || e.commandEpoch[plan.agent] != plan.commandEpoch {
			return nil
		}
		if e.nativeUnknown == nil {
			e.nativeUnknown = map[string]bool{}
		}
		for _, key := range plan.commandKeys {
			e.nativeUnknown[key] = true
		}
		for _, m := range e.state.Inbox(plan.agent) {
			if !m.Expecting() || m.From == "" || m.From == plan.agent ||
				!e.nativeUnknown["mail:"+noticeKey(plan.agent, m.Serial)] {
				continue
			}
			e.pushNoticeFor(m.From, "The native app's reply for "+plan.agent+" is unknown. Mail remains in its mailbox; "+
				"no queue fallback or automatic retry will run in this app incarnation. A new app incarnation may re-offer it.",
				e.state.Serial, m.Serial, time.Now())
		}
		return nil
	})
}
