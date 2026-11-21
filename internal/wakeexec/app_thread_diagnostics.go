// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package wakeexec

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/agenxy/dibs/internal/codexipc"
)

// AppThreadDiagnostics is observed app metadata. Optional values are absent
// when the app refused a probe or returned an incomplete page sequence.
type AppThreadDiagnostics struct {
	ThreadSource       string     `json:"thread_source,omitempty"`
	PendingCount       *int       `json:"pending_count,omitempty"`
	DibsPendingCount   *int       `json:"dibs_pending_count,omitempty"`
	OldestDibsNoticeAt *time.Time `json:"oldest_dibs_notice_at,omitempty"`
	DibsReceiptAt      *time.Time `json:"dibs_receipt_at,omitempty"`
	LastTurnStartedAt  *time.Time `json:"last_turn_started_at,omitempty"`
	NativeIPC          string     `json:"native_ipc"`
	AppOwnsThread      *bool      `json:"app_owns_thread,omitempty"`
}

// DiagnoseAppThread reads the configured app and Dibs receipt without
// opening a window, submitting input, or using private app databases.
func DiagnoseAppThread(ctx context.Context, binary, thread string) AppThreadDiagnostics {
	out := AppThreadDiagnostics{NativeIPC: "unknown"}
	if receipt := readReceipt(thread); !receipt.QueuedAt.IsZero() {
		out.DibsReceiptAt = &receipt.QueuedAt
	}
	probe, cancel := context.WithTimeout(ctx, 2*queueProbeTimeout)
	withQueueRPC(probe, binary, func(r queueRPC) {
		out.ThreadSource = r.threadSource(thread)
		out.PendingCount, out.DibsPendingCount, out.OldestDibsNoticeAt = r.pendingCounts(thread)
		out.LastTurnStartedAt = r.lastAppTurnStarted(thread)
	})
	cancel()
	native, cancel := context.WithTimeout(ctx, queueProbeTimeout)
	defer cancel()
	owns, err := codexipc.Owned(native, thread)
	switch {
	case err == nil:
		out.NativeIPC, out.AppOwnsThread = "available", &owns
	case errors.Is(err, codexipc.ErrNoSocket):
		out.NativeIPC = "no_socket"
	case errors.Is(err, codexipc.ErrAppNotListening):
		out.NativeIPC = "not_listening"
	}
	return out
}

func (r queueRPC) pendingCounts(thread string) (total, dibs *int, oldest *time.Time) {
	var cursor *string
	count, dibsCount := 0, 0
	var earliest time.Time
	for page := range 16 {
		raw, err := r.call(100+page, "thread/queue/list", map[string]any{
			"threadId": thread, "limit": 100, "cursor": cursor,
		})
		rows, next, ok := decodeQueuePage(raw)
		if err != nil || !ok {
			return nil, nil, nil
		}
		count += len(rows)
		for _, row := range rows {
			if notice, ok := row.dibsNotice(); ok {
				dibsCount++
				if at, known := noticeIssuedAt(notice); known && (earliest.IsZero() || at.Before(earliest)) {
					earliest = at
				}
			}
		}
		if next == nil {
			if !earliest.IsZero() {
				oldest = &earliest
			}
			return &count, &dibsCount, oldest
		}
		cursor = next
	}
	return nil, nil, nil
}

func (r queueRPC) lastAppTurnStarted(thread string) *time.Time {
	raw, err := r.call(120, "thread/turns/list", map[string]any{
		"threadId": thread, "limit": 1, "sortDirection": "desc", "itemsView": "notLoaded",
	})
	if err != nil {
		return nil
	}
	var result struct {
		Data []struct {
			StartedAt int64 `json:"startedAt"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &result) != nil || len(result.Data) != 1 || result.Data[0].StartedAt <= 0 {
		return nil
	}
	at := time.Unix(result.Data[0].StartedAt, 0).UTC()
	return &at
}
