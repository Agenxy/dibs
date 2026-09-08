package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// An active owner holds a stated synthetic primary and a thread alias the
// daemon inferred for it by directory. A newcomer stating both reclaims the
// guess, which is right, and used to take the stated primary with it: the
// alias's holder was recorded as authority over both ids, and the owner's
// hooks resolved to the newcomer. Each recorded field drops only its own id.
func TestReclaimingAGuessedAliasLeavesTheOwnersStatedPrimary(t *testing.T) {
	const (
		dir      = "/work/shared"
		bridge   = "host-4242"
		inferred = "19d67315-7718-491e-be3f-3864f577eeed"
	)
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	if _, err := e.HookPoll(ctx, inferred, "SessionStart", dir, false, false); err != nil {
		t.Fatal("setup:", err)
	}
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "owner", Nonce: "n-owner-0123456789ab", AgentKind: core.KindPersistent, SessionID: bridge, Agent: &core.AgentInfo{CWD: dir}}); err != nil {
		t.Fatal("setup:", err)
	}
	owner := st.Agents["owner"]
	if !owner.HoldsSessionForTest(bridge) || !owner.HoldsSessionForTest(inferred) || !owner.GuessedSession(inferred) {
		t.Fatal("setup: the owner does not hold its stated primary and the inferred alias as a guess")
	}
	// The session the guess belongs to registers, stating the bridge id and
	// its own thread.
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "newcomer", Nonce: "n-newcomer-0123456789", AgentKind: core.KindPersistent, SessionID: bridge, SessionAlias: inferred, Agent: &core.AgentInfo{CWD: t.TempDir()}}); err != nil {
		t.Fatal("setup:", err)
	}
	if !st.Agents["newcomer"].HoldsSessionForTest(inferred) || owner.HoldsSessionForTest(inferred) {
		t.Fatal("setup: the guess was not reclaimed by the session it belongs to")
	}
	if !owner.HoldsSessionForTest(bridge) {
		t.Fatal("reclaiming a guessed alias took the owner's STATED primary with it: the owner's hooks " +
			"resolve to the newcomer")
	}
	if got := st.AgentForHook(bridge, ""); got == nil || got.ID != "owner" {
		t.Fatalf("hooks quoting %s resolve to %v, not the owner", bridge, got)
	}
}
