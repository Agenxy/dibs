package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// Every agent registering through one bridge states the same synthetic
// session id, on purpose, and among several active stated holders the hook
// lookup preferred the lowest id: an agent registered later under a name
// that sorted first redirected the hooks of the agent that had the session
// first, which kept its binding and lost its routing. The one that held it
// first wins; a holder that has gone quiet still yields to a live one.
func TestANewcomerSharingABridgeSessionDoesNotTakeItsHooks(t *testing.T) {
	const host = "host-777"
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "victim", Nonce: "n-victim-0123456789", AgentKind: core.KindPersistent, SessionID: host, Agent: &core.AgentInfo{Harness: "Claude Code"}}); err != nil {
		t.Fatal("setup:", err)
	}
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "aaa", Nonce: "n-aaa-0123456789abcd", AgentKind: core.KindPersistent, SessionID: host, Agent: &core.AgentInfo{Harness: "Claude Code"}}); err != nil {
		t.Fatalf("setup: sharing a bridge session is how every harness works, and it was refused: %v", err)
	}
	if !st.Agents["victim"].HoldsSessionForTest(host) || !st.Agents["aaa"].HoldsSessionForTest(host) {
		t.Fatal("setup: both rows do not hold the shared id")
	}
	if got := st.AgentForHook(host, ""); got == nil || got.ID != "victim" {
		t.Fatalf("hooks quoting %s resolve to %v: the newcomer that sorts first took the routing from "+
			"the agent that had the session first", host, got)
	}
	// A holder that has gone quiet still yields to the live one.
	st.Agents["victim"].Status = core.StatusDormant
	if got := st.AgentForHook(host, ""); got == nil || got.ID != "aaa" {
		t.Fatalf("with the first holder dormant, hooks resolve to %v, not the live row", got)
	}
}
