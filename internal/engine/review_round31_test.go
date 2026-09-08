package engine

import (
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// The resync past the ring rebuilt only incoming mail that arrived after the
// cursor. Two other things are owed in the same gap: the verdict on a
// question THIS agent sent, which belongs to the sender's side and carries
// the question's older serial, and blocking mail an adoption moved in, whose
// own serial predates the move. Both are rebuilt.
func TestAResyncRebuildsVerdictsAndAdoptedMail(t *testing.T) {
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
	apply(&core.Op{Kind: core.OpRegister, Name: "lost", NewToken: "tok-l", V7Semantics: true}, now)
	apply(&core.Op{Kind: core.OpRegister, Name: "heir", NewToken: "tok-h", V7Semantics: true}, now)

	q := apply(&core.Op{Kind: core.OpSendMessage, Token: "tok-a", To: "busy", MsgType: core.MsgQuestion, Body: "?", DeadlineSec: 3600, V7Semantics: true}, now)
	stranded := apply(&core.Op{Kind: core.OpSendMessage, Token: "tok-a", To: "lost", MsgType: core.MsgQuestion, Body: "?", DeadlineSec: 3600, V7Semantics: true}, now)
	qSerial, _ := q["msg_serial"].(uint64)
	sSerial, _ := stranded["msg_serial"].(uint64)
	cursor := st.Serial // both questions are BEFORE the cursor

	apply(&core.Op{Kind: core.OpRespond, Token: "tok-b", MsgSerial: qSerial, Disposition: "answer", Body: "!", V7Semantics: true}, now.Add(time.Minute))
	// Adopted while the question is live: a sign_off would expire it first,
	// and adoption needs a source that is not active.
	st.Agents["lost"].Status = core.StatusDormant
	apply(&core.Op{Kind: core.OpAdoptAgent, Token: "tok-h", To: "lost", AdoptAuthorised: true, V7Semantics: true}, now.Add(3*time.Minute))
	if st.Messages[qSerial].RespondedAt <= cursor || st.Messages[sSerial].AdoptedAt <= cursor {
		t.Fatalf("setup: the verdict (%d) and the adoption (%d) must land after the cursor %d",
			st.Messages[qSerial].RespondedAt, st.Messages[sSerial].AdoptedAt, cursor)
	}

	got := map[string]uint64{}
	for _, ev := range resyncEvents(st, st.Agents["asker"], cursor) {
		serial, _ := ev.Data["msg_serial"].(uint64)
		got[ev.Type] = serial
	}
	if got["message.answered"] != qSerial {
		t.Errorf("the asker's resync carries %v: the answer to its question, given after the cursor, "+
			"is not rebuilt and the asker sleeps on a verdict it is waiting for", got)
	}
	got = map[string]uint64{}
	for _, ev := range resyncEvents(st, st.Agents["heir"], cursor) {
		serial, _ := ev.Data["msg_serial"].(uint64)
		got[ev.Type] = serial
	}
	if got["message.adopted"] != sSerial {
		t.Errorf("the heir's resync carries %v: the question adoption moved in after the cursor keeps "+
			"its older serial and is not rebuilt", got)
	}

	// Nothing owed is nothing replayed: past the verdict the asker gets none.
	if evs := resyncEvents(st, st.Agents["asker"], st.Serial); len(evs) != 0 {
		t.Errorf("a cursor at the present is handed %d event(s)", len(evs))
	}
}
