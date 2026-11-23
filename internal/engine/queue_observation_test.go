// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/wakeexec"
)

func TestBoardMarksOldPendingQueueObservationStale(t *testing.T) {
	e, ctx, _, _ := nativeAppEngine(t)
	now := time.Now().UTC()
	issued := now.Add(-time.Hour)
	for _, tc := range []struct {
		age  time.Duration
		want string
	}{
		{26 * time.Minute, "stale"},
		{0, "pending"},
	} {
		observed := now.Add(-tc.age)
		if _, err := e.query(ctx, func() core.Result {
			agent := e.state.Agents["worker"]
			e.noteQueueObservation(wakePlan{
				thread: threadIDOf(agent), createdSerial: agent.CreatedSerial, queueEpoch: e.wakers.queueEpoch,
			}, agent.ID, wakeexec.QueueObservation{
				Admission: "retained", Pending: "pending", At: observed, IssuedAt: issued, AgeSource: "notice",
			})
			return nil
		}); err != nil {
			t.Fatal("setup queue observation:", err)
		}
		board, err := e.Board(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var view core.Result
		for _, row := range board["agents"].([]map[string]any) {
			if row["id"] == "worker" {
				view, _ = row["queue_wake"].(core.Result)
			}
		}
		if view["admission"] != "retained" || view["observed_at"] != observed || view["issued_at"] != issued {
			t.Fatalf("board lost the queue observation or changed its timestamps: %v", view)
		}
		if view["pending"] != tc.want {
			t.Errorf("board pending = %v for observation aged %v, want %q", view["pending"], tc.age, tc.want)
		}
	}
}
