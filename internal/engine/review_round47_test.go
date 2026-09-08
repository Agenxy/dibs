package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// A request's adopt field is a name, resolved at approval against the
// roster of the day. A request sent before its target was purged by a
// historical sweep, which replay leaves standing, survives an upgrade; a
// stranger registers the released name, and approving the old request
// moved the stranger's mailbox. A target registered after the request was
// sent is not the agent the request concerned.
func TestApprovingAnOldAdoptionRequestCannotTakeASuccessorsMailbox(t *testing.T) {
	// ON THE ENGINE'S CLOCK, or the boot sweep expires the request before the
	// approval and a refusal for the wrong reason passes this test.
	now := time.Now()
	st := core.NewState("t", core.DefaultLimits())
	apply := func(op *core.Op, at time.Time) core.Result {
		t.Helper()
		res, _, err := st.Apply(op, at)
		if err != nil {
			t.Fatal("setup:", err)
		}
		return res
	}
	apply(&core.Op{Kind: core.OpRegister, Name: "boss", NewToken: "tok-b", V7Semantics: true}, now)
	st.Agents["boss"].Role = core.RoleCoordinator
	apply(&core.Op{Kind: core.OpRegister, Name: "requester", NewToken: "tok-r", V7Semantics: true}, now)
	apply(&core.Op{Kind: core.OpRegister, Name: "lost", NewToken: "tok-l", V7Semantics: true}, now)
	st.Agents["lost"].Status = core.StatusDormant
	req := apply(&core.Op{Kind: core.OpSendMessage, Token: "tok-r", To: "boss", MsgType: core.MsgRequest, Body: "mine", Adopt: "lost", DeadlineSec: 24 * 3600, V7Semantics: true}, now)
	serial, _ := req["msg_serial"].(uint64)
	// The historical purge: no PurgeMail flag, so the request stands.
	st.Agents["lost"].Status = core.StatusArchived
	st.Agents["lost"].ArchivedAt = now.Add(-st.Limits.ArchiveRetention - time.Hour)
	apply(&core.Op{Kind: core.OpSweep}, now)
	if st.Agents["lost"] != nil || st.Messages[serial] == nil || st.Messages[serial].Terminal() {
		t.Fatal("setup: the historical sweep did not leave the request standing over a purged row")
	}
	// A stranger takes the name, gets mail, and goes dormant.
	apply(&core.Op{Kind: core.OpRegister, Name: "lost", NewToken: "tok-l2", V7Semantics: true}, now.Add(time.Second))
	apply(&core.Op{Kind: core.OpSendMessage, Token: "tok-b", To: "lost", MsgType: core.MsgNotify, Body: "private", V7Semantics: true}, now.Add(time.Second))
	st.Agents["lost"].Status = core.StatusDormant

	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	_, err := e.Do(ctx, &core.Op{Kind: core.OpRespond, Token: "tok-b", MsgSerial: serial, Disposition: "approve"})
	var ce *core.Error
	if err == nil || !errors.As(err, &ce) || ce.Code != "E_BAD_TARGET" {
		t.Fatalf("approving a request that named a purged mailbox took the stranger's: %v", err)
	}
	if n := len(st.Inbox("lost")); n != 1 {
		t.Fatalf("the stranger's mailbox holds %d message(s), want its own 1", n)
	}
	if m := st.Messages[serial]; m.Terminal() {
		t.Fatalf("setup: the request was %s before the approval, so the refusal above proves nothing", m.State)
	}
}
