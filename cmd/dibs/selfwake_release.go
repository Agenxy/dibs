// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"log/slog"

	"github.com/agenxy/dibs/internal/mcp"
)

// An SSE replay is not a new event. Keep the attempted serial separate from
// the delivery cursor, so reconnect cannot cancel and rearm the same failure.
func (iw *inboxWatcher) firstAttempt(st *inboxStream, meta map[string]any) bool {
	serial, ok := meta[mcp.SerialMetaKey].(float64)
	if !ok || serial <= 0 {
		return true
	} // derived readiness hints have no ledger serial
	iw.mu.Lock()
	defer iw.mu.Unlock()
	if uint64(serial) <= st.attempted {
		return false
	}
	st.attempted = uint64(serial)
	return true
}

// Surrender is an event, not something the next inbox frame must discover.
// Stop all streams first, then explicitly release their authenticated route
// claims. The additive echo distinguishes support from an old daemon ignoring
// the metadata; closing the stream remains the old daemon's fallback.
func (iw *inboxWatcher) releaseClaims() {
	iw.mu.Lock()
	var streams []*inboxStream
	for _, st := range iw.streams {
		streams = append(streams, st)
	}
	iw.mu.Unlock()
	for _, st := range streams {
		if st.cancel != nil {
			st.cancel()
		}
	}
	for _, st := range streams {
		if _, _, err := st.source.readOffer(map[string]any{mcp.SelfWakeReleaseMetaKey: st.claim}); err != nil {
			slog.Warn("explicit socket handoff failed; subscription closed, original mail remains owed", "err", err)
		}
	}
}
