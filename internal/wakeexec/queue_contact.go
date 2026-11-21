// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package wakeexec

import (
	"context"
	"encoding/json"
	"time"
)

// QueueContactEvidence is the read-only verdict for one dated Dibs queue
// item. Inconclusive app or queue evidence does not authorize an alert.
type QueueContactEvidence uint8

// QueueContactWait defers an alert, Gone ends the candidate, and Ready
// confirms a quiet thread with the original queue item still pending.
const (
	QueueContactWait QueueContactEvidence = iota
	QueueContactGone
	QueueContactReady
)

// ObserveQueueContact checks the exact retained notice and the thread's
// persisted activity in one bounded, read-only app-server connection.
func ObserveQueueContact(
	ctx context.Context, binary, thread string, issuedAt, idleBefore time.Time,
) QueueContactEvidence {
	ctx, cancel := context.WithTimeout(ctx, queueProbeTimeout)
	defer cancel()
	evidence := QueueContactWait
	withQueueRPC(ctx, binary, func(rpc queueRPC) {
		p := rpc.receipt(thread, issuedAt.Truncate(time.Second))
		switch p.receipt {
		case "missing_or_dispatched", "wrong_timestamp":
			evidence = QueueContactGone
		case "matched":
			if rpc.threadQuiet(thread, idleBefore) {
				evidence = QueueContactReady
			}
		}
	})
	return evidence
}

func (r queueRPC) threadQuiet(thread string, idleBefore time.Time) bool {
	raw, err := r.call(32, "thread/read", map[string]any{"threadId": thread, "includeTurns": false})
	if err != nil {
		return false
	}
	var result struct {
		Thread struct {
			ID        string `json:"id"`
			UpdatedAt int64  `json:"updatedAt"`
			Status    struct {
				Type string `json:"type"`
			} `json:"status"`
		} `json:"thread"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Thread.ID != thread || result.Thread.UpdatedAt <= 0 {
		return false
	}
	// A separate read-only app server reports notLoaded even while the
	// desktop is working. The shared update timestamp carries the activity
	// in that case. An explicit active status always keeps the alert waiting.
	status := result.Thread.Status.Type
	return (status == "idle" || status == "notLoaded") && result.Thread.UpdatedAt <= idleBefore.Unix()
}
