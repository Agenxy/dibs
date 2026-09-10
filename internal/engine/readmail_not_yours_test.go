package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A MESSAGE THAT IS SOMEBODY ELSE'S IS NOT "NO MESSAGE".
//
// read_mail on a serial that exists, between two other agents, answered
// E_NO_MESSAGE: "no accessible message N", hint "check the serial". The same
// confident, specific and false statement the announcement case one line up
// was fixed for: a serial the caller got from a notice or a board event is not
// a serial it invented, and "no such message" sends it looking for a deletion
// that never happened. Issue #36, item four, from a live board: an agent
// concluded mail was being lost.
//
// What it is told instead reveals nothing the board does not already show:
// the two ids, which are on every roster, and never the body.
func TestReadMailOnSomebodyElsesMessageSaysWhoseItIs(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	for _, id := range []string{"a", "b", "c"} {
		st.Agents[id] = &core.Agent{
			ID: id, Name: id, Status: core.StatusActive, Token: "tok-" + id,
			Slots: map[string]core.Slot{},
		}
	}
	st.Messages[7] = &core.Message{
		Serial: 7, From: "a", To: "b", Type: core.MsgQuestion,
		State: core.MsgStatePending, Body: "the private question xq-7731",
	}
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	res, err := e.GetMessage(ctx, "tok-c", 7)
	cerr, _ := res["error"].(*core.Error)
	if cerr == nil {
		_ = errors.As(err, &cerr)
	}
	if cerr == nil {
		t.Fatalf("c read a message between a and b: %v / %v", res, err)
	}
	if cerr.Code == "E_NO_MESSAGE" {
		t.Fatalf("a message that exists was reported as not existing: %v", cerr)
	}
	for _, want := range []string{"a", "b"} {
		if !strings.Contains(cerr.Msg+cerr.Hint, want) {
			t.Errorf("the refusal does not say whose message it is (%q):\n  %s\n  %s",
				want, cerr.Msg, cerr.Hint)
		}
	}
	if strings.Contains(cerr.Msg+cerr.Hint, "xq-7731") {
		t.Error("the refusal leaked the body")
	}
}

// And a serial that genuinely does not exist is still E_NO_MESSAGE.
func TestReadMailOnAMissingSerialIsStillNoMessage(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	st.Agents["a"] = &core.Agent{
		ID: "a", Name: "a", Status: core.StatusActive, Token: "tok-a", Slots: map[string]core.Slot{},
	}
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	res, err := e.GetMessage(ctx, "tok-a", 99)
	cerr, _ := res["error"].(*core.Error)
	if cerr == nil {
		_ = errors.As(err, &cerr)
	}
	if cerr == nil || cerr.Code != "E_NO_MESSAGE" {
		t.Fatalf("a serial nothing was ever written at is not E_NO_MESSAGE: %v / %v", res, err)
	}
}
