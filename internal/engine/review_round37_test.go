package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// The bridge sends its own `host-<ppid>` as an alias on every call. A
// register that stated its thread was current on that alias instead, and
// even once the thread was current, the next check_in re-bound the alias
// and made it current again: the configured wake had no thread to resume.
// A thread beats a synthetic id; a NEW synthetic id is a new activation and
// still takes over.
func TestTheBridgesOwnIDDoesNotDisplaceAStatedThread(t *testing.T) {
	const (
		bridge = "host-4242"
		thread = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	)
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "r", Nonce: "n-r-0123456789abcdef", AgentKind: core.KindPersistent, SessionID: thread, SessionAlias: bridge, Agent: &core.AgentInfo{Harness: "Codex"}})
	if err != nil {
		t.Fatal("setup:", err)
	}
	tok, _ := res["token"].(string)
	l := st.Agents["r"]
	if !l.HoldsSessionForTest(bridge) {
		t.Fatal("setup: the bridge id was not bound as an alias, so nothing below is contested")
	}
	if got := threadIDOf(l); got != thread {
		t.Fatalf("registered stating thread %s with the bridge's %s as alias, the wake resumes %q", thread, bridge, got)
	}
	// Every later call carries the same alias.
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok, SessionAlias: bridge}); err != nil {
		t.Fatal("setup:", err)
	}
	if got := threadIDOf(l); got != thread {
		t.Fatalf("one check_in later the wake resumes %q: the bridge's own id displaced the thread", got)
	}
	// A new activation from a new bridge id, stating no thread, is still a
	// new activation: the thread it left is not the one to wake.
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "r", Nonce: "n-r-0123456789abcdef", AgentKind: core.KindPersistent, SessionID: "host-5555", SessionAlias: "host-5555", Agent: &core.AgentInfo{Harness: "Codex"}}); err != nil {
		t.Fatal("setup:", err)
	}
	if got := threadIDOf(st.Agents["r"]); got != "" {
		t.Fatalf("recovered from a new bridge id, the wake resumes %q: the activation the agent left", got)
	}
}
