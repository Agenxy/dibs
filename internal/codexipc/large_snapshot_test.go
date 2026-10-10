// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package codexipc

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/testcodexipc"
)

func TestNativeLargeHistorySnapshotStillDeliversOnce(t *testing.T) {
	for _, mode := range []string{"large-idle", "large-active"} {
		t.Run(mode, func(t *testing.T) {
			s := testcodexipc.Start(t, mode, nil)
			r, err := Deliver(context.Background(), testcodexipc.Thread, "Dibs: new notify from reviewer.")
			if err != nil {
				t.Fatalf("protocol-valid 10 MiB history blocked native delivery: %v", err)
			}
			if r.Disposition != "accepted" || len(s.Inputs()) != 1 || s.Follows() != 0 ||
				(mode == "large-active" && r.TurnID != "prior-turn") {
				t.Fatalf("large snapshot lost current state: receipt=%+v inputs=%d", r, len(s.Inputs()))
			}
		})
	}
}
