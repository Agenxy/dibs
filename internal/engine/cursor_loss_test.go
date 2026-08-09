package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/lanes/internal/core"
)

// An agent that missed events must be TOLD, not quietly resumed from wherever
// the ring happens to start now.
//
// This is the one mechanism standing between "you are up to date" and "you
// silently skipped everything that happened while you were away", and both the
// polling call and the long-poll agents sleep on have to enforce it. The clamp
// helper is unit-tested, but nothing proved the CALLERS act on a stale cursor —
// a floor check that never fires would look exactly like a healthy board.
func TestAStaleCursorIsRefusedOnBothReadPaths(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	// A small ring so the floor rises the way it does after a prune or a long
	// run, without generating 65536 events.
	e.ringCap = 4

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	res, err := e.Do(ctx, &core.Op{Kind: core.OpRegisterLane, Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := res["token"].(string)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok}); err != nil {
		t.Fatal(err)
	}
	// Push the ring well past its cap so serial 1 falls off the back.
	for i := 0; i < 12; i++ {
		if _, err := e.Do(ctx, &core.Op{Kind: core.OpSetSlot, Token: tok, Text: "work"}); err != nil {
			t.Fatal(err)
		}
	}

	// Cursor 1 is now below the floor: this agent really did miss events.
	if _, err := e.EventsSince(ctx, tok, 1, false); err == nil {
		t.Fatal("events_since must refuse a cursor that fell off the ring")
	} else if !strings.Contains(err.Error(), "E_CURSOR_TOO_OLD") {
		t.Fatalf("and must say which failure it is, got: %v", err)
	}
	if _, err := e.AwaitEvents(ctx, tok, 1, time.Second, false); err == nil {
		t.Fatal("await_events must refuse it too — this is the call agents SLEEP on, " +
			"so a silent resume here loses events with nothing awake to notice")
	} else if !strings.Contains(err.Error(), "E_CURSOR_TOO_OLD") {
		t.Fatalf("await_events must name the failure, got: %v", err)
	}

	// And the error has to be recoverable rather than terminal: ack_board is the
	// documented checkpoint, and the serial it returns must be usable.
	ack, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok})
	if err != nil {
		t.Fatal(err)
	}
	at, _ := ack["serial"].(uint64)
	if at == 0 {
		t.Fatalf("ack_board must hand back a position to resume from, got %v", ack["serial"])
	}
	if _, err := e.EventsSince(ctx, tok, at, false); err != nil {
		t.Fatalf("the serial ack_board returned must be accepted, got: %v", err)
	}
}

// Cursor 0 is what every agent reaches for before it has seen anything, and it
// is NOT a lost cursor — refusing it would make an agent's opening call fail
// with a message about ring internals it cannot know about.
func TestAFirstCursorIsNotTreatedAsLoss(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	e.ringCap = 4
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	res, err := e.Do(ctx, &core.Op{Kind: core.OpRegisterLane, Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := res["token"].(string)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		if _, err := e.Do(ctx, &core.Op{Kind: core.OpSetSlot, Token: tok, Text: "work"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.EventsSince(ctx, tok, 0, false); err != nil {
		t.Fatalf("a first read must be served from the floor, not refused: %v", err)
	}
}
