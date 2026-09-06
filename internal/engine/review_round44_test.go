package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A register whose session id and alias are both the bridge's own id was
// read as a new activation, which is right for a bridge that restarted and
// wrong for the same bridge registering again inside its TTL: the thread
// its hooks had bound stopped being the one to wake until something
// rebound it.
func TestTheSameBridgeRegisteringAgainKeepsTheBoundThread(t *testing.T) {
	const (
		bridge = "host-4242"
		thread = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	)
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	first := &core.Op{Kind: core.OpRegister, Name: "r", Nonce: "n-r-0123456789abcdef", AgentKind: core.KindPersistent, SessionID: bridge, SessionAlias: bridge, Agent: &core.AgentInfo{Harness: "Claude Code"}}
	res, err := e.Do(ctx, first)
	if err != nil {
		t.Fatal("setup:", err)
	}
	tok, _ := res["token"].(string)
	// The hooks bind the real thread.
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok, SessionAlias: thread}); err != nil {
		t.Fatal("setup:", err)
	}
	if got := threadIDOf(st.Agents["r"]); got != thread {
		t.Fatalf("setup: after the hook bound the thread the wake resumes %q", got)
	}
	// The same bridge, the same registration again.
	again, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "r", Nonce: "n-r-0123456789abcdef", AgentKind: core.KindPersistent, SessionID: bridge, SessionAlias: bridge, Agent: &core.AgentInfo{Harness: "Claude Code"}})
	if err != nil {
		t.Fatal("setup:", err)
	}
	if again["resumed"] != true && again["reattached"] != true {
		t.Fatalf("setup: the repeat did not resume: %v", again)
	}
	if got := threadIDOf(st.Agents["r"]); got != thread {
		t.Fatalf("the same bridge registering again leaves the wake resuming %q: the thread its "+
			"hooks bound is no longer the one to wake", got)
	}
}
