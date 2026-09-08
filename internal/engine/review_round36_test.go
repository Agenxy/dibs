package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A register carrying neither token nor nonce was refused the thread alias
// an active row already held, then reattached to that very row by name and
// session id: the fold took the synthetic host id as current, the thread
// the row still held was no longer the one to wake, and the configured
// route stood down until an authenticated call bound it again. Re-asserting
// your own thread is not theft.
func TestAReattachByNameKeepsTheThreadItReasserts(t *testing.T) {
	const (
		host   = "host-4242"
		thread = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	)
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	first, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "r", AgentKind: core.KindPersistent, SessionID: host, SessionAlias: thread, Agent: &core.AgentInfo{Harness: "Codex"}})
	if err != nil {
		t.Fatal("setup:", err)
	}
	if first["nonce"] == nil || first["nonce"] == "" {
		t.Fatal("setup: the daemon minted no nonce for the first registration")
	}
	if got := threadIDOf(st.Agents["r"]); got != thread {
		t.Fatalf("setup: after registering the wake resumes %q, want the stated thread", got)
	}
	// The same session, registering again with no credential: the recovery
	// path the agent is told to use when it has lost its context.
	again, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "r", AgentKind: core.KindPersistent, SessionID: host, SessionAlias: thread, Agent: &core.AgentInfo{Harness: "Codex"}})
	if err != nil {
		t.Fatal("setup:", err)
	}
	if again["reattached"] != true {
		t.Fatalf("setup: the second registration did not reattach: %v", again)
	}
	if got := threadIDOf(st.Agents["r"]); got != thread {
		t.Fatalf("reattached by name and session id, the wake resumes %q: the thread the row still "+
			"holds and the register re-asserted is no longer the one to wake", got)
	}
}
