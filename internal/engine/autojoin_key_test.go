package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/overlap"
)

// attemptJoin must carry the coordination key OUT, not just cause one to exist.
//
// core has always returned the key on a join; the engine's matcher threw it
// away, so an agent joined automatically by declare became a member of an agent
// it could not name exactly, and the key is precisely what it would declare in
// `refs` next time to be matched by identity instead of by wording. The agent
// that got there by being guessed at was the one left unable to stop being
// guessed at.
//
// Tested HERE rather than in core, and that distinction is the whole point. A
// core test of the same behaviour passes with this engine fix reverted, because
// core was never the broken half: it is the same "correct and dead" trap
// admit_wired_test.go exists for. Verified by reverting: this fails, that does
// not.
//
// And not in the space e2e either: an automatic join needs a board where a
// shared identifying ref is the strongest match, and in a suite with accumulated
// agents the second agent matched something else entirely. A test that must win a
// scoring contest to reach its assertion is not testing what it says.
func TestTheMatcherCarriesTheCoordinationKeyOutOfAnAutoJoin(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
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
	owner, joiner := reg("alpha"), reg("beta")

	opened, err := e.Do(ctx, &core.Op{
		Kind: core.OpSpaceOpen, Token: owner, Space: "auth-work", Text: "auth",
	})
	if err != nil {
		t.Fatalf("setup: open_space: %v", err)
	}
	want, _ := opened["key"].(string)
	if want == "" {
		t.Fatal("setup: open_space returned no key; this test is pinning the wrong field")
	}

	action, _, key := e.attemptJoin(ctx, joiner,
		core.AgentMatch{Agent: "auth-work", Score: 0.9},
		MatchConfig{JoinThreshold: 0.33},
		overlap.Prediction{ScorerID: "test"}, nil)

	if action != "joined" {
		t.Fatalf("attemptJoin action = %q, want joined: the assertion below cannot "+
			"mean anything without an actual join", action)
	}
	if key != want {
		t.Errorf("attemptJoin returned key %q, want %q: the matcher joined the agent "+
			"and kept the one thing that would let it declare the agent exactly", key, want)
	}
}
