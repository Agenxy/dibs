package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// whoHoldsSession resolves a session id ON the loop. Reading e.state from a
// test goroutine while the writer is running is a data race, and -race is part
// of the gate.
func whoHoldsSession(t *testing.T, ctx context.Context, e *Engine, sid string) string {
	t.Helper()
	res, err := e.query(ctx, func() core.Result {
		if l := e.state.AgentBySession(sid); l != nil {
			return core.Result{"id": l.ID}
		}
		return core.Result{"id": ""}
	})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res["id"].(string)
	return id
}

// A harness that names its own thread is believed, once vetted.
//
// Codex sends `threadId` in _meta on every tools/call, unconditionally and to
// every MCP server, and the value is the id `codex resume` takes. So the stable
// identity arrives on the connection the agent already holds. Measured:
//
//	session id:  01a01209-a872-75d0-92c8-af0adf0d7712
//	threadId  =  01a01209-a872-75d0-92c8-af0adf0d7712
//
// Before this, the daemon inferred a session from the directory an agent
// registered in, which is a correlation and wrong whenever two agents share a
// checkout. This is an identity, and it beats the guess.
func TestAHarnessNamedThreadIsBound(t *testing.T) {
	e, ctx, cancel := runningEngine(t)
	defer cancel()

	const thread = "01a01209-a872-75d0-92c8-af0adf0d7712"
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "codex-agent", AgentKind: core.KindPersistent,
		Nonce: "n-c", SessionID: "host-4242", SessionAlias: thread,
		Agent: &core.AgentInfo{CWD: "/repo/app"},
	}); err != nil {
		t.Fatal("setup:", err)
	}

	// The harness's own id now finds the agent, which is what a lifecycle hook
	// and a ring both need.
	if got := whoHoldsSession(t, ctx, e, thread); got != "codex-agent" {
		t.Fatalf("the thread id resolves to %q, not the agent that named it", got)
	}
	// And the bridge's own name still does, because both are true.
	if got := whoHoldsSession(t, ctx, e, "host-4242"); got != "codex-agent" {
		t.Errorf("binding the thread id lost the bridge's name for the same session (got %q)", got)
	}
}

// One agent may not take a thread another agent already holds.
//
// The claim arrives on an authenticated connection but it is still a claim, and
// binding somebody else's session would move their wake delivery onto the
// claimant: the same disclosure the announced-session join refuses. Whoever
// holds it keeps it.
func TestAThreadAnotherAgentHoldsIsNotTaken(t *testing.T) {
	e, ctx, cancel := runningEngine(t)
	defer cancel()

	const thread = "01a01209-a872-75d0-92c8-af0adf0d7712"
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "incumbent", AgentKind: core.KindPersistent,
		Nonce: "n-1", SessionAlias: thread, Agent: &core.AgentInfo{CWD: "/repo/app"},
	}); err != nil {
		t.Fatal("setup:", err)
	}
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "claimant", AgentKind: core.KindPersistent,
		Nonce: "n-2", SessionAlias: thread, Agent: &core.AgentInfo{CWD: "/repo/app"},
	}); err != nil {
		t.Fatal("setup:", err)
	}

	if got := whoHoldsSession(t, ctx, e, thread); got != "incumbent" {
		t.Errorf("the thread moved to %q: a second agent naming a thread somebody "+
			"else holds would take delivery of their wakes", got)
	}
}
