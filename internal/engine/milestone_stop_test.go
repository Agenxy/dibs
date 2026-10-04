package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestStopDeliversProgressOnceWithoutRequiringReview(t *testing.T) {
	for _, event := range []string{"Stop", "SubagentStop"} {
		t.Run(event, func(t *testing.T) {
			e := New(core.NewState("milestone-stop", core.DefaultLimits()), &memLedger{}, deadProber{})
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			go e.Run(ctx)
			do := func(op *core.Op) core.Result {
				t.Helper()
				r, err := e.Do(ctx, op)
				if err != nil {
					t.Fatalf("setup %s: %v", op.Kind, err)
				}
				return r
			}
			lead := do(&core.Op{Kind: core.OpRegister, Name: "lead", Nonce: "lead-stop", SessionID: "lead-stop-session"})["token"].(string)
			worker := do(&core.Op{Kind: core.OpRegister, Name: "worker", Nonce: "worker-stop"})["token"].(string)
			do(&core.Op{Kind: core.OpAckBoard, Token: lead})
			parent := do(&core.Op{Kind: core.OpSendMessage, Token: lead, To: "worker", MsgType: core.MsgRequest, Body: "proof", Milestones: []string{"proof"}})["msg_serial"].(uint64)
			do(&core.Op{Kind: core.OpRespond, Token: worker, MsgSerial: parent, Disposition: "approve"})
			if _, err := e.GetMessage(ctx, lead, parent); err != nil {
				t.Fatal(err)
			}
			do(&core.Op{Kind: core.OpRespond, Token: worker, MsgSerial: parent, Disposition: "progress", Milestone: 1, Body: "ready"})
			if got, err := e.HookPoll(ctx, "lead-stop-session", "UserPromptSubmit", "", false, false); err != nil || deliveredSomething(got) {
				t.Fatalf("non-delivering prompt event spent progress: %v %v", got, err)
			}
			stop := func() core.Result {
				t.Helper()
				r, err := e.HookPoll(ctx, "lead-stop-session", event, "", false, false)
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
			first := stop()
			if first["decision"] != "block" || !strings.Contains(first["reason"].(string), "reports progress") {
				t.Fatalf("first Stop must deliver progress: %v", first)
			}
			// Expire the presentation cadence, without acting on or reviewing mail.
			// Otherwise an immediate second Stop tests only the existing two-minute throttle.
			if _, err := e.query(ctx, func() core.Result {
				for k := range e.noticePresented {
					e.noticePresented[k] = time.Now().Add(-2 * AnnounceRetry)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if second := stop(); second["decision"] == "block" {
				t.Fatalf("already delivered progress blocked again: %v", second)
			}
			if _, err := e.query(ctx, func() core.Result {
				if len(e.state.Messages[parent].Progress) != 1 {
					t.Error("delivery invented a review")
				}
				if len(e.pendingNotices("lead")) != 0 || e.state.Messages[parent].OutcomeReadAt == 0 {
					t.Error("fully quoted Stop report was not durably read")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
