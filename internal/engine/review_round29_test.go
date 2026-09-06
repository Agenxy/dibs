package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A persistent agent recovered by nonce from a new `host-<ppid>` activation
// still holds the uuid of the activation it left. The wake resumed that
// thread, a real one and the wrong one, while the activation waiting for
// its mail stayed asleep. The current activation is known and is not a
// thread, so no thread is known for it and the exec route stands down.
func TestARecoveryFromANewHostSessionDoesNotResumeTheOldThread(t *testing.T) {
	const (
		oldHost   = "host-111"
		newHost   = "host-222"
		oldThread = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	)
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "r", Nonce: "n-recover-0123456789", AgentKind: core.KindPersistent, SessionID: oldHost, SessionAlias: oldThread, Agent: &core.AgentInfo{Harness: "Codex"}}); err != nil {
		t.Fatal("setup:", err)
	}
	if got := threadIDOf(st.Agents["r"]); got != oldThread {
		t.Fatalf("setup: before the recovery the wake resumes %q, want the bound thread", got)
	}
	res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "r", Nonce: "n-recover-0123456789", AgentKind: core.KindPersistent, SessionID: newHost, Agent: &core.AgentInfo{Harness: "Codex"}})
	if err != nil || (res["reattached"] != true && res["resumed"] != true) {
		t.Fatalf("setup: the nonce did not recover the row: %v %v", res, err)
	}
	l := st.Agents["r"]
	if l.CurrentSession != newHost {
		t.Fatalf("setup: after the recovery the current session is %q, want %s", l.CurrentSession, newHost)
	}
	if got := threadIDOf(l); got != "" {
		t.Fatalf("recovered from %s, the wake resumes %s: the thread the agent left, while "+
			"the activation waiting for its mail stays asleep", newHost, got)
	}
}
