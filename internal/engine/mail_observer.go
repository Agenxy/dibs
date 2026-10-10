// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// ObserveInbox is the human panel's mailbox read, including private refreshes.
// All observer callers use this door: displaying a body to a human does not
// prove that the recipient model received it. Neither delivery, FYI consumption,
// outcome reads nor model activity may be recorded here. The human view retains
// its complete mailbox; compact model reads use InboxPage instead.
func (e *Engine) ObserveInbox(ctx context.Context, token string) (core.Result, error) {
	return e.query(ctx, func() core.Result {
		now := time.Now()
		l, refused := e.authObserve(token, now)
		if refused != nil {
			return refused
		}
		mail := e.state.Inbox(l.ID)
		res := core.Result{
			"inbox": mail, "messages": mail, "serial": e.state.Serial,
			"truncated_before_serial": l.TruncatedBefore,
			"announcements":           e.state.UnackedFor(l.ID), "task_queue": e.taskQueueView(l.ID),
			"owes": e.owedSerials(l.ID, now), "owed_work": e.owedWorkView(l.ID, now),
		}
		if gone := e.state.UnanswerableSenders(mail); len(gone) > 0 {
			res["unanswerable_senders"] = gone
		}
		return res
	})
}
