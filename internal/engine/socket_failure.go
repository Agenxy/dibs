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
		key := l.ID + "\x00" + strconv.FormatUint(m.Serial, 10)
		if !e.socketWritten["mail:"+key] && wanted[key] && e.socketActionableMessage(m) {
			version = max(version, m.Serial)
		}
	}
	for _, n := range e.takeNotices(l.ID) {
		key := noticeKey(l.ID, n.Serial)
		at, shown := e.noticePresented[key]
		if !e.socketWritten["notice:"+key] && !n.Delivered && (!shown || now.Sub(at) >= AnnounceRetry) &&
			socketActionableNotice(n, l, e.state.Messages[n.Msg]) {
			version = max(version, n.Serial)
		}
	}
	for _, a := range e.state.Unacked(l.ID) {
		if !e.socketWritten["announcement:"+noticeKey(l.ID, a.Serial)] {
			version = max(version, a.Serial)
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
		if version != plan.socketVersion {
			return nil // a newer original cause did not take part in this failure
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
