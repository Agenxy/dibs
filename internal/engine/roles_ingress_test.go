package engine

import (
	"context"
	"testing"
	"time"

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

// An ARCHIVED coordinator must not go on refusing the bootstrap claim.
//
// The predicate spelled its own condition out rather than asking the resolver,
// and excluded only StatusClosed. Archiving is a different terminal state, and
// it blanks the token and the nonce; resume refuses an archived identity, and
// core.CoordinatorID already ignores archived holders. So the board's real
// answer to "who coordinates?" was "nobody", while the claim guard answered
// "that one" and refused the only route back. A board could be left with no
// usable coordinator and no way to claim one: found by the pre-release review.
//
// The fix is not a second status in the list. It is asking CoordinatorID, so
// that the guard and the resolver cannot drift apart again.
func TestAnArchivedCoordinatorDoesNotStrandTheBoard(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	now := time.Now()
	e := New(st, &memLedger{}, deadProber{})

	if _, _, err := st.Apply(&core.Op{
		Kind: core.OpRegister, Name: "fleet-lead", Nonce: "n-fleet-lead",
		AgentKind: core.KindPersistent,
	}, now); err != nil {
		t.Fatalf("setup: register: %v", err)
	}
	id := st.CoordinatorID()
	if id != "" {
		t.Fatalf("setup: a fresh board already reports a coordinator (%s)", id)
	}
	for agentID := range st.Agents {
		id = agentID
	}
	if _, _, err := st.Apply(&core.Op{
		Kind: core.OpGrantRole, To: id, Mode: core.RoleCoordinator,
	}, now); err != nil {
		t.Fatalf("setup: grant: %v", err)
	}
	if st.CoordinatorID() != id {
		t.Fatalf("setup: the grant did not take, so the rest of this proves nothing")
	}
	claim := &core.Op{Kind: core.OpClaimCoordinator}
	if err := e.refuseClaimWhenCoordinatorExists(claim); err == nil {
		t.Fatal("setup: a live coordinator did not refuse the claim, so this test " +
			"cannot tell the archived case from the guard being gone entirely")
	}

	// Now archive it, the way the sweep does to an agent nobody came back for.
	st.Agents[id].Status = core.StatusArchived

	if st.CoordinatorID() != "" {
		t.Fatalf("setup: the resolver still names an archived coordinator")
	}
	if err := e.refuseClaimWhenCoordinatorExists(claim); err != nil {
		t.Errorf("an archived coordinator still refuses the bootstrap claim (%v), so "+
			"a board whose only coordinator was swept has no way to get another: "+
			"its token and nonce are blank and resume refuses archived identities", err)
	}
}
