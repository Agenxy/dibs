package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// The human stays the human across a restart.
//
// The identity used to be held only as this daemon run's token, so every
// restart un-personed the operator until they unlocked again. Everything keyed
// off it then went quiet: the board stopped marking their row, and mail
// addressed to them stopped raising a notification. That last one is the path
// that exists BECAUSE the person is not in a loop, so its absence is exactly
// the kind nobody notices.
//
// Caught by deploying the notification work and watching the board fail to mark
// anybody as the human one restart later.
func TestTheHumanIsStillTheHumanAfterARestart(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	led := &memLedger{}
	first := New(st, led, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go first.Run(ctx)

	id, _, err := first.HumanAgent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("setup: no human agent was minted")
	}
	if got := first.HumanIdentity(); got != id {
		t.Fatalf("HumanIdentity = %q before any restart, want %q", got, id)
	}

	// A new engine over the SAME state is what a restart looks like from here:
	// the ledger and the board survive, the run's in-memory token does not.
	second := New(st, led, deadProber{})
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go second.Run(ctx2)

	if got := second.HumanIdentity(); got != id {
		t.Errorf("after a restart HumanIdentity = %q, want %q. The board would stop "+
			"marking the operator's row and mail addressed to them would stop reaching "+
			"them, with nothing saying so", got, id)
	}
}

// Reading the board must still not make anybody a participant. Recovering an
// EXISTING registration is reading what is already there; minting one on a
// board that has never had a human is not.
func TestAnUnusedBoardHasNoHuman(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	if got := e.HumanIdentity(); got != "" {
		t.Errorf("HumanIdentity = %q on a board nobody has unlocked: opening the board "+
			"put somebody on the roster", got)
	}
}
