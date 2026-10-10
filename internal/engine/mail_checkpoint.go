// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func (e *Engine) prepareMailboxPage(op *core.Op, actor *core.Agent, now time.Time) (*mailPage, error) {
	if op.Kind != core.OpAckBoard || !op.MailboxPage || actor == nil {
		return nil, nil
	}
	p, err := e.mailboxPage(actor, op.MailboxCursor, op.MailboxLimit, now)
	if err != nil {
		return nil, err
	}
	ids := []uint64{}
	for _, m := range p.mail {
		ids = append(ids, m.Serial)
	}
	op.MailboxSerials = &ids
	// Older folds record only an ordinary contact checkpoint. They do not
	// stamp omitted mail delivered or refuse a safe rollback.
	op.Kind = core.OpActivityCheckpoint
	return &p, e.state.Admit(op)
}

// check_in returns situational notices before clearing their derived cache.
// Outcomes and reviews are read only through the complete rendered prefix.
func (e *Engine) completeMailboxCheckpoint(
	res core.Result, actor *core.Agent, page *mailPage, now time.Time,
) error {
	res["task_queue"] = e.taskQueueView(actor.ID)
	res["owes"] = e.owedSerials(actor.ID, now)
	res["owed_work"] = e.owedWorkView(actor.ID, now)
	if page != nil {
		e.putMailboxPage(res, actor, *page)
	}
	pending, err := e.pullUpdates(actor, now)
	if err != nil {
		return err
	}
	restart, err := e.readAppRestart(actor.Token, now)
	if err != nil {
		return err
	}
	if restart != "" {
		pending = append(pending, restart)
	}
	if pending == nil {
		pending = []string{}
	}
	res["agent_updates"], res["serial"] = pending, e.state.Serial
	// Recovery must say whether overlap detection is operating at all.
	if st := e.MatchStatus(); st.Phase != MatchReady {
		res["matching"], res["matching_hint"] = st.Phase, matchingHint(st)
	}
	if page != nil {
		if err := e.presentMailboxFYIs(res, actor, *page, now); err != nil {
			return err
		}
		e.refreshMailboxReceipts(res, actor, *page)
		res["serial"] = e.state.Serial
	}
	return nil
}

func (e *Engine) mailboxUpdates(l *core.Agent, now time.Time) ([]string, error) {
	updates, err := e.pullUpdates(l, now)
	if err != nil {
		return nil, err
	}
	restart, err := e.readAppRestart(l.Token, now)
	if err != nil {
		return nil, err
	}
	if restart != "" {
		updates = append(updates, restart)
	}
	return updates, nil
}
