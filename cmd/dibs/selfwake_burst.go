// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"log/slog"
	"time"
)

type bridgeWakeArrival struct {
	stream *inboxStream
	meta   map[string]any
	line   string
}

// The fixed window starts at the first new event, never resets on replay,
// and the one shared writer refreshes the whole owned batch when it closes.
func (iw *inboxWatcher) queueWake(st *inboxStream, meta map[string]any, line string, waker *selfWaker) {
	iw.mu.Lock()
	defer iw.mu.Unlock()
	iw.arrivals = append(iw.arrivals, bridgeWakeArrival{st, meta, line})
	if iw.burst != nil || iw.sending {
		return
	}
	iw.burst = time.AfterFunc(200*time.Millisecond, func() { iw.flushWakeBatch(waker) })
}

func (iw *inboxWatcher) flushWakeBatch(waker *selfWaker) {
	iw.mu.Lock()
	batch := iw.arrivals
	iw.arrivals, iw.burst, iw.sending = nil, nil, true
	iw.mu.Unlock()
	defer func() {
		iw.mu.Lock()
		defer iw.mu.Unlock()
		iw.sending = false
		if len(iw.arrivals) > 0 && waker.canReach() {
			iw.burst = time.AfterFunc(200*time.Millisecond, func() { iw.flushWakeBatch(waker) })
		}
	}()
	if len(batch) == 0 || !waker.canReach() {
		return
	}
	if err := waker.wake(batch[len(batch)-1].line); err != nil {
		slog.Debug("could not put a batched notice into this session; keeping its cursor", "err", err)
		return
	}
	for _, arrival := range batch {
		iw.noteSerial(arrival.stream, arrival.meta)
	}
}
