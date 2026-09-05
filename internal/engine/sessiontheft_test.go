package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// A UUID victimThread stands in for a real Codex thread id, which is what
// wakeFor substitutes into the operator's resume command.
const victimThread = "3f2504e0-4f89-11d3-9a0c-0305e82c3301"

// An agent must not be able to claim another agent's THREAD as its session.
//
// register and bind_session both take a caller-supplied session_id and the fold
// wrote it down unconditionally. wakeFor then turns a UUID-shaped session id
// into the thread argument of the operator's `[wake.exec]` command, so an agent
// that knew a peer's thread UUID could assert it and have the board resume that
// peer's thread; hook resolution for the victim went ambiguous at the same
// time. Reported by the pre-release review, which reproduced it against HEAD.
//
// BOTH DOORS, in one test, because fixing one of two identical routes is a
// mistake this release has already made once: the adoption note was corrected
// on adopt_agent and left wrong on the approved-request path. register needs no
// prior binding at all, so it is the easier of the two to walk through.
func TestAnotherAgentsThreadCannotBeClaimed(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	// The victim, holding its own thread id.
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "victim", Nonce: "n-victim",
		AgentKind: core.KindPersistent, SessionID: victimThread,
	}); err != nil {
		t.Fatalf("setup: registering the victim: %v", err)
	}
	// Setup must hold, or everything below is asserting about nothing.
	if holder := st.AgentBySession(victimThread); holder == nil {
		t.Fatal("setup: the victim does not hold its own thread id, so there is " +
			"nothing here for an attacker to take")
	}

	t.Run("register cannot assert it", func(t *testing.T) {
		_, err := e.Do(ctx, &core.Op{
			Kind: core.OpRegister, Name: "attacker-a", Nonce: "n-a",
			AgentKind: core.KindPersistent, SessionID: victimThread,
		})
		if err == nil {
			t.Fatal("a brand new agent registered under the victim's thread id. The " +
				"board will now resume that thread on this agent's behalf, and hook " +
				"delivery for the victim is ambiguous")
		}
		if !strings.Contains(err.Error(), victimThread) {
			t.Errorf("the refusal does not name the id it refused, so the caller "+
				"cannot tell which of its arguments was the problem: %v", err)
		}
	})

	// THE NAME IS PUBLIC; THE NONCE IS THE CREDENTIAL.
	//
	// The first version of this guard stood aside whenever the supplied NAME
	// matched the holder's, on the theory that it was the row reattaching to
	// itself before it had a token. A name is on the board for anyone to read.
	// So the bypass was: the victim's name, the victim's thread id, and a fresh
	// nonce of your own. The name matched, the guard allowed it, and the fold
	// then took neither reattachment branch, because the nonce was not the
	// victim's, and minted a SIBLING holding the victim's thread.
	//
	// My own regression test above missed it because its attacker used the name
	// "attacker-a", and the same-name case I did write supplied the correct
	// nonce. Neither covered same name with the wrong one. Found by the
	// pre-release review.
	t.Run("the victim's name with an attacker's own nonce", func(t *testing.T) {
		before := len(st.Agents)
		_, err := e.Do(ctx, &core.Op{
			Kind: core.OpRegister, Name: "victim", Nonce: "n-not-the-victims",
			AgentKind: core.KindPersistent, SessionID: victimThread,
		})
		if err == nil {
			t.Error("an agent registered under the victim's NAME with its own nonce and " +
				"took the victim's thread id. The board now has two live agents bound " +
				"to one thread: hook lookup is ambiguous and waking the sibling resumes " +
				"the victim's session")
		}
		if got := len(st.Agents); got != before {
			t.Errorf("the agent count moved from %d to %d, so a sibling was created",
				before, got)
		}
	})

	t.Run("bind_session cannot assert it either", func(t *testing.T) {
		res, err := e.Do(ctx, &core.Op{
			Kind: core.OpRegister, Name: "attacker-b", Nonce: "n-b",
			AgentKind: core.KindPersistent,
		})
		if err != nil {
			t.Fatalf("setup: registering the attacker: %v", err)
		}
		tok, _ := res["token"].(string)
		if tok == "" {
			t.Fatal("setup: no token, so the bind below proves nothing")
		}
		if _, err := e.BindSession(ctx, tok, victimThread); err == nil {
			t.Error("an agent bound the victim's thread id with its own token, which " +
				"redirects the operator's wake command at that thread")
		}
	})

	// The victim itself is unaffected: it still holds the id it came with.
	if holder := st.AgentBySession(victimThread); holder == nil || holder.Name != "victim" {
		t.Errorf("the victim no longer holds its own thread id: %v", holder)
	}
}

// And the sharing that is DELIBERATE must keep working.
//
// This is why the guard tests looksLikeThreadID rather than uniqueness. The
// stdio bridge derives `host-<ppid>` from the harness process, so every agent
// registering through one bridge presents the SAME session id on purpose, and
// mcpstdio_session.go argues for that at length. A plain "session ids must be
// unique" rule, which is the obvious reading of the finding, would have refused
// the second agent in every ordinary harness on the machine.
func TestAgentsSharingOneBridgeSessionAreStillAllowed(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	const bridge = "host-5360" // what the bridge derives; not thread-shaped
	if looksLikeThreadID(bridge) {
		t.Fatal("setup: the bridge id looks like a thread id, so this test is not " +
			"exercising the distinction it exists for")
	}

	for _, name := range []string{"first", "second"} {
		if _, err := e.Do(ctx, &core.Op{
			Kind: core.OpRegister, Name: name, Nonce: "n-" + name,
			AgentKind: core.KindPersistent, SessionID: bridge,
		}); err != nil {
			t.Fatalf("agent %q could not register through a bridge session another "+
				"agent already uses, which is how every harness works: %v", name, err)
		}
	}
}

// Reattaching to your own thread is not theft.
//
// A persistent agent coming back with the same name and nonce resolves to the
// row that already holds this id. Refusing that would make the guard break the
// recovery path it is meant to protect, which is the shape of half the entries
// in this repository's changelog.
func TestAnAgentMayReassertItsOwnThread(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	first := &core.Op{
		Kind: core.OpRegister, Name: "comeback", Nonce: "n-comeback",
		AgentKind: core.KindPersistent, SessionID: victimThread,
	}
	if _, err := e.Do(ctx, first); err != nil {
		t.Fatalf("setup: first registration: %v", err)
	}
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "comeback", Nonce: "n-comeback",
		AgentKind: core.KindPersistent, SessionID: victimThread,
	}); err != nil {
		t.Errorf("an agent could not re-register with its own name, nonce and thread "+
			"id, so the guard has broken reattachment: %v", err)
	}
}

// And a board that already recorded such a binding must still boot.
//
// This is the hazard that governs where the guard lives. Apply is the fold, and
// Ledger.Replay calls it directly rather than going through Engine.exec, so a
// refusal placed in Apply would refuse ops that were legal when they were
// written and the daemon would decline to start on its own history. That has
// happened here before, which is why the coordinator guard beside this one
// carries the same warning.
//
// Verified rather than assumed: this drives core.State.Apply, the way replay
// does, and both bindings must fold.
func TestTheFoldStillAcceptsABindingTheIngressNowRefuses(t *testing.T) {
	st := core.NewState("replay", core.DefaultLimits())
	now := t0Engine()

	if _, _, err := st.Apply(&core.Op{
		Kind: core.OpRegister, Name: "victim", NewToken: "tok-victim",
		SessionID: victimThread,
	}, now); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// The op an older build would have written down: a second agent taking the
	// same thread id. The ingress refuses this now; the fold must not.
	if _, _, err := st.Apply(&core.Op{
		Kind: core.OpRegister, Name: "older-build", NewToken: "tok-older",
		SessionID: victimThread,
	}, now); err != nil {
		t.Fatalf("the fold refused a binding an older build accepted and ledgered. "+
			"Replay calls Apply directly, so this is a daemon that will not start "+
			"on its own history: move the check to the ingress. %v", err)
	}
}

func t0Engine() time.Time { return time.Unix(1700000000, 0) }
