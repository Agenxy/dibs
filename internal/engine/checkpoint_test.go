package engine

import (
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// check_in is documented as the recovery checkpoint: the call an agent makes
// after losing its context, to learn what it still owes and what was done to it
// while it was away.
//
// Both of those keys used to be OMITTED when there was nothing to report, so the
// answer to "what happened to me?" was silence: indistinguishable from the
// feature being broken, or from having asked on the wrong agent. The first agent
// to use it as a recovery checkpoint reported exactly that: "returned no
// `announcements` or `agent_updates` keys at all, absent, not empty, though its
// description says it returns them."
//
// A checkpoint has to answer, including with nothing.
func TestAckBoardAlwaysAnswersWithBothKeys(t *testing.T) {
	st := core.NewState("n1", core.DefaultLimits())
	if _, _, err := st.Apply(&core.Op{
		Kind: core.OpRegister, Name: "solo", NewToken: "tok-solo",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	e := New(st, nopLedger{}, nil)

	res, err := e.exec(&core.Op{Kind: core.OpAckBoard, Token: "tok-solo"}, time.Now())
	if err != nil {
		t.Fatalf("check_in: %v", err)
	}

	ann, ok := res["announcements"]
	if !ok {
		t.Error("announcements key absent: an agent cannot tell 'nothing owed' from 'broken'")
	} else if ann == nil {
		t.Error("announcements is nil; want an empty list")
	}

	upd, ok := res["agent_updates"]
	if !ok {
		t.Fatal("agent_updates key absent: this is the one that says what was done TO you")
	}
	got, isSlice := upd.([]string)
	if !isSlice {
		t.Fatalf("agent_updates = %T, want a slice so 'nothing' is expressible", upd)
	}
	if len(got) != 0 {
		t.Errorf("want no updates on a fresh agent, got %v", got)
	}
}

// nopLedger accepts everything: this test is about what check_in RETURNS, and a
// real ledger would only add a temp directory to the failure modes.
type nopLedger struct{}

func (nopLedger) Append(uint64, time.Time, *core.Op) error { return nil }
