// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

type failedDelivery struct {
	version uint64
	keys    []string
}

// Snapshot identifiers only, on the writer, before an actual delivery attempt.
// A later unrelated message cannot turn an acknowledged failure into a retry.
func (e *Engine) failedDeliveryKeys(agent string) []string {
	return e.deliveryKeysAt(agent, time.Now())
}

func (e *Engine) deliveryKeysAt(agent string, now time.Time) []string {
	var keys []string
	for _, key := range e.wakeKeys(agent, now) {
		keys = append(keys, "mail:"+key)
	}
	for _, key := range e.dueNoticeKeys(agent, now) {
		keys = append(keys, "notice:"+key)
	}
	_, announcements := e.dueAnnouncements(agent, now)
	for _, key := range announcements {
		keys = append(keys, "announcement:"+key)
	}
	return keys
}

func (e *Engine) failedCauseOutstanding(agent string, failed []string) bool {
	if len(failed) == 0 || e.state == nil || e.state.Agents[agent].Retired() {
		return false
	}
	live := map[string]bool{}
	for _, key := range e.failedDeliveryKeys(agent) {
		live[key] = true
	}
	for _, key := range failed {
		if live[key] {
			return true
		}
	}
	return false
}

func (e *Engine) armFirstFailedWake(attempt int, agent string, keys []string, after time.Duration) {
	if attempt < 2 {
		e.armFailedWake(agent, keys, after)
	}
}

// The only wake timer: one retry after a failed kernel/command delivery. The
// caller's attempt counter caps execution at two; refusal never rearms it.
func (e *Engine) armFailedWake(agent string, keys []string, after time.Duration) {
	if len(keys) == 0 {
		return
	}
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	if e.wakers.failedCauses == nil {
		e.wakers.failedCauses = map[string]failedDelivery{}
	}
	if e.wakers.deferred == nil {
		e.wakers.deferred = map[string]*time.Timer{}
	}
	if timer := e.wakers.deferred[agent]; timer != nil {
		timer.Stop()
	}
	e.wakers.retryVersion++
	version := e.wakers.retryVersion
	e.wakers.failedCauses[agent] = failedDelivery{version: version, keys: append([]string(nil), keys...)}
	e.wakers.deferred[agent] = time.AfterFunc(after+50*time.Millisecond, func() {
		_ = e.peerSessions()
		e.retryFailedDelivery(agent, version)
	})
}

func (e *Engine) retryFailedDelivery(agent string, version uint64) {
	_, _ = e.query(context.Background(), func() core.Result {
		e.wakers.mu.Lock()
		failed := e.wakers.failedCauses[agent]
		if failed.version != version {
			e.wakers.mu.Unlock()
			return nil
		}
		delete(e.wakers.failedCauses, agent)
		delete(e.wakers.deferred, agent)
		e.wakers.mu.Unlock()
		if e.failedCauseOutstanding(agent, failed.keys) {
			e.retryWakeDecision(agent)
		}
		return nil
	})
}
