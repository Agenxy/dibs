package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// The migration that moves this computer's existing rows onto the fleet
// identity it has just adopted has to survive the ingress gate, and the
// gate is where it died.
//
// `dibd` submits OpHostRenamed with no token, because the daemon is acting
// as itself; ingress refuses a tokenless op that is not on the system list,
// and the op was not on it. Every startup migration returned E_BAD_TOKEN
// and was logged as a warning nobody reads, so rows registered before the
// adoption kept the old id and rows registered after carried the new one:
// two machines, one computer, each able to hold an exclusive claim on the
// same path.
//
// The test that shipped with the fold called State.Apply directly, which is
// the production path with the failing half removed. This one goes through
// e.Do, which is what the daemon calls. Round thirty-seven of the
// pre-release review.
func TestTheHostMigrationSurvivesTheIngressGate(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "before", SessionID: "session-before",
		Agent: &core.AgentInfo{HostID: "host-9001"},
	}); err != nil {
		t.Fatalf("setup: register: %v", err)
	}

	res, err := e.Do(ctx, &core.Op{Kind: core.OpHostRenamed, HostWas: "host-9001", HostNow: "fleet-id"})
	if err != nil {
		t.Fatalf("the daemon's own startup migration was refused at ingress: %v; "+
			"a tokenless op the daemon generates belongs on the system list", err)
	}
	if n, _ := res["changed"].(int); n != 1 {
		t.Fatalf("migration reported changed=%v, want 1 agent moved: %+v", res["changed"], res)
	}

	var moved bool
	onLoop(t, ctx, e, func(s *core.State) {
		a := s.Agents["before"]
		moved = a != nil && a.Agent != nil && a.Agent.HostID == "fleet-id"
	})
	if !moved {
		t.Fatal("the row registered before the adoption still carries the old host id")
	}
}

// And the gate's other half still holds: presenting a token with a system
// op is positive evidence that a REQUEST is being replayed as the daemon,
// so adding this kind to the list must not open a door for an agent that
// somehow reaches Do with one.
func TestTheHostMigrationIsRefusedWhenItCarriesAToken(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	res, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "someone", SessionID: "session-someone",
	})
	if err != nil {
		t.Fatalf("setup: register: %v", err)
	}
	tok, _ := res["token"].(string)

	_, err = e.Do(ctx, &core.Op{
		Kind: core.OpHostRenamed, Token: tok, HostWas: "host-9001", HostNow: "mine",
	})
	if !errors.Is(err, core.ErrBadToken) {
		t.Fatalf("an agent's token was accepted on the daemon's own migration op: err=%v", err)
	}
}
