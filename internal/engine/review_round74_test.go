package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// The alias guard asks registerLandsOn whether the fold will land this
// register on that row, and that question reaches pickReattachTarget, which
// answers by the HISTORICAL rule while V7Semantics is unset. The flag was
// stamped two hundred lines below the guard, so the guard judged by v0.0.6's
// rule and discarded a thread the fold then accepted by v0.0.7's. The row
// reattached and its wake target silently stayed on the older thread.
func TestRecoveryKeepsTheThreadTheHarnessIsActuallyIn(t *testing.T) {
	threadA := "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	threadB := "019ffe52-0eaf-7f60-81cc-6ab1298d76ed"
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	// Registered under a synthetic host id, in thread A.
	reg, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "worker", NewToken: "tok-1",
		SessionID: "host-4242", SessionAlias: threadA,
		Agent: &core.AgentInfo{CWD: "/work"},
	})
	if err != nil {
		t.Fatal("setup:", err)
	}
	tok, _ := reg["token"].(string)

	// An authenticated call binds thread B: the harness has moved on.
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok, SessionAlias: threadB}); err != nil {
		t.Fatal("setup:", err)
	}
	if got := st.Agents["worker"].CurrentSession; got != threadB {
		t.Fatalf("setup: the current session is %q, not thread B", got)
	}

	// It recovers with no credential, naming the OLD THREAD as its session
	// while the harness supplies the thread it is really in. Naming the
	// synthetic host id here instead reaches the row by the legacy rule too,
	// so the guard and the fold agree and the defect never appears: that is
	// the version of this test that passed against it.
	res, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "worker", NewToken: "tok-2",
		SessionID: threadA, SessionAlias: threadB,
		Agent: &core.AgentInfo{CWD: "/work"},
	})
	if err != nil {
		t.Fatal("recover:", err)
	}
	if res["agent_id"] != "worker" {
		t.Fatalf("the recovery minted a sibling (%v) rather than reattaching", res["agent_id"])
	}
	if got := st.Agents["worker"].CurrentSession; got != threadB {
		t.Errorf("after recovering, the wake target is %q rather than thread B: the guard threw "+
			"away the thread the harness is in, judging by a rule the fold did not use, so "+
			"every wake goes to a thread the agent has left", got)
	}
}
