package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// A notice's age has to survive the restart that rebuilds it.
//
// Notices are engine-ephemeral and rebuilt from state on boot, which is the
// architecture's rule for a derived view. The nudge built on them reports how
// long the oldest thing has been waiting, and it takes that from the event that
// produced the notice.
//
// The rebuilt event is CONSTRUCTED in rebuildBlockingNotices rather than
// replayed, so it carries only the fields written there. Leave TS out and every
// restored notice comes back with no time, the nudge goes back to printing
// identical bytes forever, and the habituation the age exists to remove returns
// on every restart: a fix that works right up until the first time the daemon
// is bounced, which for a long-lived board is most of its life.
//
// rebuildBlockingNotices' own comment already demands more than this: "an agent
// must not be able to tell that its board restarted from the wording of its own
// mail". An age that silently disappears tells it.
//
// Caught reviewing my own change, which had introduced exactly this.
func TestARebuiltNoticeKeepsTheAgeOfWhatHappened(t *testing.T) {
	answered := time.Unix(1700000000, 0)
	// Long enough after the verdict to be past the floor, so an age is owed.
	now := answered.Add(4 * time.Hour)

	st := core.NewState("t", core.DefaultLimits())
	ap := func(op *core.Op) core.Result {
		r, _, err := st.Apply(op, answered)
		if err != nil {
			t.Fatalf("setup %s: %v", op.Kind, err)
		}
		return r
	}
	for _, n := range []string{"asker", "answerer"} {
		res := ap(&core.Op{Kind: core.OpRegister, Name: n, NewToken: "tok-" + n})
		id, _ := res["agent_id"].(string)
		ap(&core.Op{Kind: core.OpAckBoard, Token: st.Agents[id].Token})
	}
	q := ap(&core.Op{
		Kind: core.OpSendMessage, Token: st.Agents["asker"].Token, To: "answerer",
		MsgType: core.MsgQuestion, Body: "may I", DeadlineSec: 600,
	})
	ap(&core.Op{
		Kind: core.OpRespond, Token: st.Agents["answerer"].Token,
		MsgSerial: q["msg_serial"].(uint64), Disposition: "answer", Body: "yes",
	})

	// A cold engine over that state: the restart.
	e := &Engine{state: st}
	e.rebuildBlockingNotices()

	// Setup has to hold, or the assertion below is about nothing: the restart
	// must actually have restored a notice for the asker.
	if n := len(e.pendingNotices("asker")); n != 1 {
		t.Fatalf("the rebuild restored %d notice(s) for the asker, wanted 1: there is "+
			"no restored notice here to carry an age", n)
	}

	if got := e.oldestNotice("asker"); got.IsZero() {
		t.Fatal("the restored notice carries no time, so the nudge built on it has no " +
			"age to report and reads identically forever after any restart")
	}
	line := e.waiting("asker", now)
	if line == "" {
		t.Fatal("nothing is reported as waiting, so there is no nudge to check")
	}
	if !strings.Contains(line, "has been waiting") {
		t.Errorf("four hours after the verdict the restored notice produces a nudge "+
			"with no age, which is the habituation the age exists to remove coming "+
			"back on every restart\n  %s", line)
	}
	// And the age must be the age of the VERDICT, not of the restart. Rendered
	// in hours past the first, so four hours reads as "4h".
	if !strings.Contains(line, "4h") {
		t.Errorf("the age is not the four hours since the verdict, so the rebuild is "+
			"stamping the notice with the time it was restored and reporting old news "+
			"as fresh\n  %s", line)
	}
}
