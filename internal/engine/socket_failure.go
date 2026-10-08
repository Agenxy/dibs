// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"strconv"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// A failed native write gets the same one retry as an ordinary wake. The
// readiness tick must not create an unlimited retry path alongside it. Only
// a changed actionable cohort or real turn evidence rearms this derived cap.
type socketFailure struct {
	version uint64
	count   int
	at      time.Time
}

func (e *Engine) socketDaemonReady(l *core.Agent, now time.Time) bool {
	rec := e.socketFailures[socketSessionKey(l)]
	if rec.count == 0 || rec.version != e.socketCauseVersion(l, now) {
		return true
	}
	return rec.count < 2 && !now.Before(rec.at.Add(e.recencyWindow(l)))
}

func (e *Engine) socketCauseVersion(l *core.Agent, now time.Time) uint64 {
	var version uint64
	key := socketSessionKey(l)
	for _, row := range e.state.Agents {
		if row.Retired() || socketSessionKey(row) != key {
			continue
		}
		version = max(version, e.socketMailVersion(row, now))
		_, work := e.dueSocketWaits(row, now)
		for _, due := range work {
			version = max(version, due.version)
		}
		version = max(version, e.socketBackoff[row.ID].version)
	}
	return version
}

func (e *Engine) socketMailVersion(l *core.Agent, now time.Time) uint64 {
	var version uint64
	wanted := map[string]bool{}
	for _, key := range e.wakeKeys(l.ID, now) {
		wanted[key] = true
	}
	for _, m := range e.state.Inbox(l.ID) {
		if wanted[l.ID+"\x00"+strconv.FormatUint(m.Serial, 10)] && e.socketActionableMessage(m) {
			version = max(version, m.Serial)
		}
	}
	for _, n := range e.takeNotices(l.ID) {
		at, shown := e.noticePresented[l.ID+"\x00"+strconv.FormatUint(n.Serial, 10)]
		if !n.Delivered && (!shown || now.Sub(at) >= AnnounceRetry) &&
			socketActionableNotice(n, l, e.state.Messages[n.Msg]) {
			version = max(version, n.Serial)
		}
	}
	return version
}

func (e *Engine) recordSocketOutcome(plan wakePlan, written bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = e.query(ctx, func() core.Result {
		l := e.state.Agents[plan.agent]
		if l == nil || !l.SessionIsCurrent(plan.session) {
			return nil
		}
		key := socketSessionKey(l)
		if written {
			delete(e.socketFailures, key)
			return nil
		}
		now := time.Now()
		version := e.socketCauseVersion(l, now)
		if version != plan.socketVersion || e.socketLifecycle(l, now) == "busy" {
			return nil // a new turn or new cause did not take part in this failure
		}
		rec := e.socketFailures[key]
		if rec.version != version {
			rec = socketFailure{version: version}
		}
		rec.count++
		rec.at = now
		if e.socketFailures == nil {
			e.socketFailures = map[string]socketFailure{}
		}
		e.socketFailures[key] = rec
		return nil
	})
}
