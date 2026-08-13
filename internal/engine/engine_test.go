package engine

import (
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// One op, one serial: the invariant whose breach took a live board down.
//
// Apply finishes centrally, and several handlers finish for themselves because
// they need the serial in their result. open_space therefore allocated TWO for
// one op, and the engine appends at the final value, so the intermediate
// serial was never written: a permanent hole in the ledger at a point where a
// real transition had happened.
//
// The consequence was not cosmetic. One of those holes held the op that
// re-created an agent, so on restart the daemon replayed a board where that agent
// was still closed, met a sign_off it could not apply, and refused to start
// with no way back. The serial-gap WARNING had been firing for weeks and reads
// as housekeeping; it was the symptom.
//
// Asserted over the family that shares the pattern rather than only open_space,
// which is merely the one that got caught.
func TestOneOpAllocatesExactlyOneSerial(t *testing.T) {
	s := core.NewState("n1", core.DefaultLimits())
	now := time.Now()
	if _, _, err := s.Apply(&core.Op{
		Kind: core.OpRegister, Name: "serialprobe", NewToken: "tok",
	}, now); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, _, err := s.Apply(&core.Op{Kind: core.OpAckBoard, Token: "tok"}, now); err != nil {
		t.Fatalf("ack: %v", err)
	}

	for _, op := range []*core.Op{
		{Kind: core.OpSpaceOpen, Token: "tok", Space: "serial-work", Text: "work"},
		{Kind: core.OpSetSlot, Token: "tok", Text: "declaring something"},
		{Kind: core.OpSpaceAnnounce, Token: "tok", Space: "serial-work", Body: "heads up"},
		{Kind: core.OpSpaceLeave, Token: "tok", Space: "serial-work"},
	} {
		before := s.Serial
		if _, evs, err := s.Apply(op, now); err != nil {
			t.Fatalf("%s: %v", op.Kind, err)
		} else if evs == nil {
			continue // no state change, no serial, correctly not ledgered
		}
		if got := s.Serial - before; got != 1 {
			t.Errorf("%s advanced the serial by %d, want exactly 1: anything more "+
				"leaves a hole in the ledger where a transition happened",
				op.Kind, got)
		}
	}
}
