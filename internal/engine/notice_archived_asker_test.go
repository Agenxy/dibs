package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// A verdict owed to an ARCHIVED asker survives a restart.
//
// Archived is idle, not retired: this release makes an archived agent
// resumable for exactly the case of an answer arriving while it was away.
// The rebuild still asked `Gone()`, which is closed OR archived, so the
// answer's notice was not restored and no boot wake went out; the answer
// stayed readable by serial with nothing pointing at it. The respond path
// told the answerer the same story, that the asker "closed its agent before
// this arrived", about an agent that had merely gone quiet. Closed is the
// only state with nobody left to tell. Found by the pre-release review,
// round five.
func TestAVerdictOwedToAnArchivedAskerIsRebuiltAfterARestart(t *testing.T) {
	answered := time.Unix(1700000000, 0)
	st := core.NewState("t", core.DefaultLimits())
	ap := func(op *core.Op) core.Result {
		r, _, err := st.Apply(op, answered)
		if err != nil {
			t.Fatalf("setup %s: %v", op.Kind, err)
		}
		return r
	}
	for _, n := range []string{"asker", "answerer"} {
		res := ap(&core.Op{Kind: core.OpRegister, Name: n, NewToken: "tok-" + n, Nonce: "n-" + n})
		id, _ := res["agent_id"].(string)
		ap(&core.Op{Kind: core.OpAckBoard, Token: st.Agents[id].Token})
	}
	q := ap(&core.Op{
		Kind: core.OpSendMessage, Token: st.Agents["asker"].Token, To: "answerer",
		MsgType: core.MsgQuestion, Body: "may I", DeadlineSec: 600,
	})
	// The asker goes quiet long enough to be archived by the sweep's timer.
	st.Agents["asker"].Status = core.StatusArchived
	st.Agents["asker"].ArchivedAt = answered
	res := ap(&core.Op{
		Kind: core.OpRespond, Token: st.Agents["answerer"].Token,
		MsgSerial: q["msg_serial"].(uint64), Disposition: "answer", Body: "yes",
	})
	if note, _ := res["note"].(string); strings.Contains(note, "closed its agent") {
		t.Errorf("the answerer was told the asker closed its agent; it is archived, which "+
			"is idle, and it can resume and read this: %q", note)
	}

	e := &Engine{state: st}
	e.rebuildBlockingNotices()
	if n := len(e.pendingNotices("asker")); n != 1 {
		t.Fatalf("the rebuild restored %d notice(s) for the archived asker, wanted 1: its "+
			"answer is readable by serial and nothing will ever point at it", n)
	}
}
