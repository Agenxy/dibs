package engine

import (
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// The notice rebuild at construction runs before the boot sweep, and the
// sweep deletes consumed terminal mail past its retention: an answered
// request older than that produced a blocking notice at boot and lost its
// message a moment later, so the agent was woken, told to read_mail(N), and
// answered E_NO_MESSAGE, on every restart. A notice that points at mail the
// state no longer holds is dropped after each sweep.
func TestABootSweepDropsTheNoticesForTheMailItDeletes(t *testing.T) {
	now := time.Unix(1700000000, 0)
	st := core.NewState("t", core.DefaultLimits())
	apply := func(op *core.Op, at time.Time) core.Result {
		t.Helper()
		res, _, err := st.Apply(op, at)
		if err != nil {
			t.Fatal("setup:", err)
		}
		return res
	}
	apply(&core.Op{Kind: core.OpRegister, Name: "asker", NewToken: "tok-a", V7Semantics: true}, now)
	apply(&core.Op{Kind: core.OpRegister, Name: "busy", NewToken: "tok-b", V7Semantics: true}, now)
	q := apply(&core.Op{Kind: core.OpSendMessage, Token: "tok-a", To: "busy", MsgType: core.MsgQuestion, Body: "?", DeadlineSec: 3600, V7Semantics: true}, now)
	serial, _ := q["msg_serial"].(uint64)
	apply(&core.Op{Kind: core.OpRespond, Token: "tok-b", MsgSerial: serial, Disposition: "answer", Body: "!", V7Semantics: true}, now.Add(time.Minute))

	// The restart, long after the answer: the rebuild owes the asker a notice.
	e := New(st, &memLedger{}, deadProber{})
	if e.blockingNotices("asker") != 1 {
		t.Fatalf("setup: the rebuild owes the asker %d notice(s), want 1", e.blockingNotices("asker"))
	}
	later := now.Add(st.Limits.ConsumedRetention + time.Hour)
	e.boot(later)
	if st.Messages[serial] != nil {
		t.Fatalf("setup: the boot sweep kept message %d, so the notice below points at mail that exists", serial)
	}
	if n := e.blockingNotices("asker"); n != 0 {
		t.Fatalf("after the boot sweep deleted message %d the asker still owes %d notice(s) pointing at it: "+
			"it will be woken to read_mail(%d) and answered E_NO_MESSAGE", serial, n, serial)
	}
}
