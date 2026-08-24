package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// The published verification is an END-TO-END claim, so something has to test
// it end to end.
//
// The Codex plugin tells an operator to call spawned_agents across a turn
// boundary and watch `state` move to "finished". That claim spans four things:
// the hook reaches HookPoll, HookPoll calls announceHookSession, that calls
// noteChild with StateForEvent's answer, and Children publishes the result.
//
// The guard that existed asserted StateForEvent("Stop") == "finished" and read
// through a test-only childrenSnapshot. Both halves are real, and neither
// touches three of those four links: cut the call to announceHookSession, or
// stop publishing `state` from Children, and the operator's verification starts
// passing for a hook that never arrives while the guard stays green. That is
// the failure the guard was written about, one layer out from where it looked.
//
// Found by a pre-release review, which is the second time this exact procedure
// has been the thing that could not fail.
func TestALifecycleEventReachesThePublishedChildState(t *testing.T) {
	e := New(core.NewState("test", core.DefaultLimits()), &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	const session = "the-session-an-operator-would-compare"

	// Through the real entry point, the one a harness hook calls.
	stateAfter := func(event string) string {
		t.Helper()
		if _, err := e.HookPoll(ctx, session, event, "/tmp/work", false, false); err != nil {
			t.Fatalf("HookPoll(%s): %v", event, err)
		}
		res, err := e.Children(ctx)
		if err != nil {
			t.Fatalf("Children after %s: %v", event, err)
		}
		kids, _ := res["children"].([]map[string]any)
		for _, c := range kids {
			if c["session_id"] == session {
				s, _ := c["state"].(string)
				return s
			}
		}
		t.Fatalf("after %s the published children do not mention %q at all, so an "+
			"operator following the verification has nothing to compare: %v",
			event, session, res["children"])
		return ""
	}

	running := stateAfter("SessionStart")
	if running != "running" {
		t.Errorf("after SessionStart the published state is %q, not \"running\". The "+
			"operator is told to look at this field before the boundary", running)
	}

	finished := stateAfter("Stop")
	if finished != "finished" {
		t.Errorf("after Stop the published state is %q, not \"finished\". The whole "+
			"verification is: run a turn, and watch this become finished", finished)
	}
	if running == finished {
		t.Error("the published state is the same before and after the turn boundary, " +
			"so comparing it cannot tell a working hook from a missing one, which is " +
			"the only thing the procedure is for")
	}
}
