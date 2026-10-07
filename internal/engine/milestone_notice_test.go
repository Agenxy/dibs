// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A sender reviewing a milestone has acted on the progress notice without
// needing a separate read_mail. Enter through the same respond operation.
func TestMilestoneVerdictClearsOnlyTheProgressItAnswers(t *testing.T) {
	for _, verdict := range []string{"accept", "flag"} {
		t.Run(verdict, func(t *testing.T) {
			e := New(core.NewState("milestones", core.DefaultLimits()), &memLedger{}, deadProber{})
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			go e.Run(ctx)
			do := func(op *core.Op) core.Result {
				t.Helper()
				r, err := e.Do(ctx, op)
				if err != nil {
					t.Fatalf("%s setup: %v", op.Kind, err)
				}
				return r
			}
			tokens := map[string]string{}
			for _, id := range []string{"sender", "worker"} {
				tokens[id] = do(&core.Op{Kind: core.OpRegister, Name: id, Nonce: "milestone-notice-fixture-" + id})["token"].(string)
				do(&core.Op{Kind: core.OpAckBoard, Token: tokens[id]})
			}
			var serials []uint64
			for range 2 {
				s := do(&core.Op{Kind: core.OpSendMessage, Token: tokens["sender"], To: "worker", MsgType: core.MsgRequest, Body: "do this"})["msg_serial"].(uint64)
				serials = append(serials, s)
				do(&core.Op{Kind: core.OpRespond, Token: tokens["worker"], MsgSerial: s, Disposition: "approve", Milestones: []string{"proof"}})
				// Consume the approval before checking the new progress notice.
				if _, err := e.GetMessage(ctx, tokens["sender"], s); err != nil {
					t.Fatal(err)
				}
				do(&core.Op{Kind: core.OpRespond, Token: tokens["worker"], MsgSerial: s, Disposition: "progress", Milestone: 1, Body: "proof ready"})
			}
			check := func(want int) {
				t.Helper()
				if _, err := e.query(ctx, func() core.Result {
					got := e.takeNotices("sender")
					if len(got) != want {
						t.Errorf("pending progress notices = %d, want %d: %v", len(got), want, got)
					}
					if want == 1 && len(got) == 1 && got[0].Msg != serials[1] {
						t.Error("verdict cleared another request's progress")
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			check(2)
			do(&core.Op{Kind: core.OpRespond, Token: tokens["sender"], MsgSerial: serials[0], Disposition: verdict, Milestone: 1, Body: "reviewed"})
			check(1)
		})
	}
}
