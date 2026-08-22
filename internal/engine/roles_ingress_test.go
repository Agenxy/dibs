package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A launch claim must stop working once the board has a coordinator.
//
// The claim file is minted at startup when none exists, and nothing retired it
// once one did. A role declared in dibs.toml is granted a few seconds later, so
// an ordinary startup left a live claim in the data directory beside a board
// that already had its coordinator: a second persistent agent that read the
// file could consume it and acquire broadcast, force_release, eviction and
// mailbox adoption. Reproduced against the production startup order by the
// pre-release review.
//
// This drives the ingress predicate directly, because the flag that reaches
// applyClaimCoordinator is CLEARED at ingress and re-derived from the real
// claim file: a test cannot assert its way past that, which is the point of
// clearing it. That the verified path reaches this guard is covered by
// TestAClaimIsRefusedOnceTheRoleIsHeld in cmd/dibd, where the claim is wired.
func TestTheIngressRefusesAClaimOnceACoordinatorExists(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	claim := &core.Op{Kind: core.OpClaimCoordinator}
	if err := e.refuseClaimWhenCoordinatorExists(claim); err != nil {
		t.Fatalf("a board with no coordinator refused its own bootstrap claim: %v", err)
	}

	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "fleet-lead", Nonce: "n-fleet-lead",
		AgentKind: core.KindPersistent,
	}); err != nil {
		t.Fatalf("setup: register: %v", err)
	}
	if _, err := e.GrantRole(ctx, "fleet-lead", core.RoleCoordinator); err != nil {
		t.Fatalf("setup: granting the declared role: %v", err)
	}

	if err := e.refuseClaimWhenCoordinatorExists(claim); err == nil {
		t.Error("the launch claim is still admitted on a board that already has a " +
			"coordinator, so a second agent can take broadcast, force_release, " +
			"eviction and mailbox adoption")
	}
}
