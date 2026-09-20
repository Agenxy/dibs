package engine

import (
	"context"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// A verdict the asker has READ is not handed back after a restart.
//
// read_mail cleared the notice from the live map and wrote nothing
// replayable, so the rebuild, which asks whether the asker's awareness
// watermark has passed the verdict, restored it: only check_in moves that
// watermark. A daemon restarted between the read and the next check_in
// delivered the same instruction again, and an instruction that does not
// clear when obeyed teaches an agent that the channel nags. Issue #76.
//
// A REBUILT ENGINE over the REPLAYED LEDGER, which is what a restart is, and
// the read goes through the real read_mail path so the test proves the
// record is written, not only that the rebuild would honour one set by hand.
// The first version rebuilt from a copy of the live state, which a read that
// wrote its record in memory only would also have satisfied: the one
// mutation the test exists to exclude. Round six of the pre-release review.
func TestAReadVerdictIsNotHandedBackAfterARestart(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	led := &memLedger{}
	e := New(st, led, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	reg := func(name string) string {
		res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: name})
		if err != nil {
			t.Fatalf("setup: register %s: %v", name, err)
		}
		tok, _ := res["token"].(string)
		if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok}); err != nil {
			t.Fatalf("setup: ack %s: %v", name, err)
		}
		return tok
	}
	asker, answerer := reg("asker"), reg("answerer")
	sent, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: asker, To: "answerer",
		MsgType: core.MsgQuestion, Body: "may I?",
	})
	if err != nil {
		t.Fatalf("setup: send: %v", err)
	}
	serial, _ := sent["msg_serial"].(uint64)
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRespond, Token: answerer, MsgSerial: serial, Disposition: "answer", Body: "yes",
	}); err != nil {
		t.Fatalf("setup: respond: %v", err)
	}

	// Before the read, a restart owes the asker the verdict: that is the
	// rebuild working, and the baseline this test's claim rests on.
	if n := New(replayed(t, ctx, e, led), &memLedger{}, deadProber{}).blockingNotices("asker"); n == 0 {
		t.Fatal("setup: a restart before the read rebuilt no notice, so the assertion " +
			"below would pass against a rebuild that does nothing")
	}

	// The asker reads its verdict, through read_mail.
	res, err := e.GetMessage(ctx, asker, serial)
	if err != nil || res["error"] != nil {
		t.Fatalf("read_mail: %v %v", err, res["error"])
	}

	// The restart. An agent must not be able to tell that its board restarted
	// from the wording of its own mail, and being told to read what it has
	// just read is exactly that tell.
	if n := New(replayed(t, ctx, e, led), &memLedger{}, deadProber{}).blockingNotices("asker"); n != 0 {
		t.Errorf("%d blocking notice(s) rebuilt for a verdict the asker read before "+
			"the restart: read_mail cleared it in memory only, and every restart "+
			"between a read and the next check_in hands it back", n)
	}
}

// replayed folds the ops the engine ledgered into a fresh state, the way a
// restart does: nothing the live engine changed in memory alone survives.
func replayed(t *testing.T, ctx context.Context, e *Engine, led *memLedger) *core.State {
	t.Helper()
	var ops []*core.Op
	_, _ = e.query(ctx, func() core.Result { // the ledger is written on the loop
		ops = append([]*core.Op(nil), led.ops...)
		return core.Result{}
	})
	st := core.NewState("t", core.DefaultLimits())
	for i, op := range ops {
		if _, _, err := st.Apply(op, time.Unix(1700000000+int64(i), 0)); err != nil {
			t.Fatalf("replaying op %d (%s): %v", i, op.Kind, err)
		}
	}
	return st
}
