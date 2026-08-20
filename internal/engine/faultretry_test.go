package engine

import (
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A fault found before anybody was on the board still reaches them.
//
// THE SILENCE THIS ENDS. The startup reachability check runs before any agent
// has registered, so there is no coordinator and no human to tell. ReportFault
// correctly refuses to mark the kind seen when it could not deliver, but
// nothing ever tried again: the coordinator who joined a minute later heard
// nothing, and the operator's machine went on being unable to raise a
// notification with no one told. Found by a pre-release review.
//
// Held now, and flushed by the sweep, which is the one thing that runs without
// an agent's call to hang it on.
func TestAFaultFoundBeforeAnybodyArrivedIsStillDelivered(t *testing.T) {
	e, ctx, cancel := runningEngine(t)
	defer cancel()

	// An empty board: nobody to tell.
	e.ReportFault(ctx, Fault{
		Kind: "notify-unreachable", What: "cannot reach the person", Remedy: "install the app",
	})
	e.faults.mu.Lock()
	held := len(e.faults.pending)
	seen := e.faults.seen["notify-unreachable"]
	e.faults.mu.Unlock()
	if held != 1 {
		t.Fatalf("the fault was dropped rather than held: pending = %d", held)
	}
	if seen {
		t.Fatal("the kind was marked seen without anybody being told, which suppresses " +
			"it for the rest of the run")
	}

	// Somebody arrives, and the sweep runs.
	res, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "coord", AgentKind: core.KindPersistent,
		Nonce: "coord-nonce", NewToken: "tok-coord",
	})
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := res["token"].(string)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok}); err != nil {
		t.Fatal(err)
	}
	id, _ := res["agent_id"].(string)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpGrantRole, To: id, Mode: core.RoleCoordinator}); err != nil {
		t.Fatalf("grant: %v", err)
	}

	// Called directly rather than through flushFaults, which spawns this and
	// returns: the split exists because flushFaults runs ON the writer loop and
	// this half asks the loop questions. Testing the half that does the work is
	// the point (AGENTS.md), and waiting on a goroutine would test the timing.
	e.deliverHeldFaults()

	e.faults.mu.Lock()
	stillHeld := len(e.faults.pending)
	delivered := e.faults.seen["notify-unreachable"]
	e.faults.mu.Unlock()
	if stillHeld != 0 {
		t.Errorf("the fault is still held with a coordinator on the board: %d", stillHeld)
	}

	// DELIVERY, not merely an empty queue.
	//
	// This asserted only that `pending` emptied, which deleting the ReportFault
	// call entirely would also satisfy, and so would every delivery failing.
	// The queue draining is the thing that happens either way; the mail arriving
	// is the thing this feature is for. A pre-release review made exactly that
	// point.
	if !delivered {
		t.Error("the fault was dropped from the queue without being marked delivered")
	}
	inbox := e.state.Inbox(id)
	if len(inbox) == 0 {
		t.Fatal("the coordinator's inbox is empty: the held fault was removed from " +
			"the queue and never arrived, which is the silence this whole path exists " +
			"to end")
	}
	var found bool
	for _, m := range inbox {
		if strings.Contains(m.Body, "cannot reach the person") {
			found = true
		}
	}
	if !found {
		t.Errorf("the coordinator has %d messages and none is the held fault", len(inbox))
	}
}
