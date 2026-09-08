package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// An agent that says nothing about its kind gets one that can park and come back.
//
// Ephemeral was the default, and ephemeral means: swept to `stale` rather than
// `dormant` when the session ends, no durable mailbox, and no nonce, which is
// the only credential that recovers an identity. So the default opted an agent
// out of every guarantee this product exists to make, silently, at the one call
// where nobody is thinking about it.
//
// The evidence was self-erasing, which is why it survived so long: counting the
// kinds of the agents still ON the board says almost nobody uses ephemeral,
// because ephemeral agents are exactly the ones no longer there. Found when a
// test agent registered twice in one afternoon and had evaporated both times,
// and then a wake resolved to the thread it had been running in, reached an
// identity whose mailbox was empty, and truthfully reported "no mail".
func TestAnUnstatedKindIsPersistent(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "quiet"})
	if err != nil {
		t.Fatal(err)
	}
	l := st.Agents["quiet"]
	if l == nil {
		t.Fatal("no agent was created")
	}
	if l.Kind != core.KindPersistent {
		t.Errorf("an agent that stated no kind got %q. It cannot park and come back, "+
			"cannot be woken, and its mail dies with its process", l.Kind)
	}
	// And it must be TOLD the credential, or it is an orphan by construction:
	// a persistent row whose nonce nobody knows can never be reattached.
	nonce, _ := res["nonce"].(string)
	if nonce == "" {
		t.Fatal("no nonce was returned, so the agent cannot ever come back as " +
			"itself: a persistent row nobody holds the credential for is exactly " +
			"the orphan adopt_agent exists to clean up after")
	}
	if nonce != l.Nonce {
		t.Errorf("the nonce handed to the agent is not the one recorded")
	}
	if h, _ := res["nonce_hint"].(string); !strings.Contains(strings.ToUpper(h), "KEEP") {
		t.Errorf("the nonce arrives with no instruction to keep it: %q", h)
	}

	// It really recovers: same name, same nonce, new process.
	back, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "quiet", Nonce: nonce})
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := back["agent_id"].(string); id != "quiet" {
		t.Errorf("re-registering with the returned nonce made %q rather than "+
			"coming back as itself", id)
	}
}

// A caller's own nonce is not echoed back at it.
//
// It already holds the secret; repeating it buys nothing and writes it into one
// more transcript. Only a nonce this daemon invented has to be handed over.
func TestAChosenNonceIsNotEchoed(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	res, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "brought-my-own",
		AgentKind: core.KindPersistent, Nonce: "mine-and-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, echoed := res["nonce"]; echoed {
		t.Error("a nonce the caller chose was echoed back to them")
	}
}

// Ephemeral is still available to anything that asks for it by name.
//
// The category is real for a genuinely disposable worker, and more to the point
// this ledger is full of ephemeral rows: the fold has to keep understanding the
// kind whatever the default becomes.
func TestEphemeralIsStillHonouredWhenAsked(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "throwaway",
		AgentKind: core.KindEphemeral,
	}); err != nil {
		t.Fatal(err)
	}
	if got := st.Agents["throwaway"].Kind; got != core.KindEphemeral {
		t.Errorf("an explicit ephemeral registration became %q", got)
	}
}

// A minted nonce must not break recovery by session_id.
//
// The first version of persistent-by-default wrote the minted nonce straight
// into `op.Nonce`, and that field is not a value: it is a CLAIM. A non-empty
// one selects the nonce reattach path and disqualifies the session_id one, so
// every registration suddenly looked like an agent presenting a credential
// nobody had ever seen. Context-loss recovery stopped reattaching and forked a
// sibling instead, which is the single failure this project warns about most
// loudly and the one its own operator has been bitten by.
//
// Three unit tests of mine passed against that, because each asked whether the
// new agent was persistent and none asked whether the OLD one could still come
// back. The space e2e caught it in a run, by doing the obvious thing.
func TestAMintedNonceDoesNotBreakSessionReattach(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	first, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "worker", SessionID: "s-1"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Agents["worker"] == nil || st.Agents["worker"].Nonce == "" {
		t.Fatal("setup: the agent has no nonce at all, so this proves nothing")
	}

	// Context loss: same name, same session, no credential in hand.
	// Asserted BEFORE anything about how the nonce was recorded: the behaviour
	// is the point, and a test that trips on its own bookkeeping first reports
	// the wrong failure for the right bug.
	back, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "worker", SessionID: "s-1"})
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := back["agent_id"].(string); id != "worker" {
		t.Fatalf("re-registering after context loss made %q instead of coming back "+
			"as itself: a sibling that cannot read its predecessor's mail", id)
	}
	if back["reattached"] != true {
		t.Errorf("it came back but was not told it had reattached: %v", back)
	}
	// And the reattach must not hand out a second secret: the agent already has
	// one, and echoing a new one into the transcript teaches it to keep the
	// wrong value.
	if _, echoed := back["nonce"]; echoed {
		t.Error("a reattach returned a nonce. Only a registration that CREATES an " +
			"agent mints one; anything else is handing a returning agent a " +
			"credential that is not the one its row is filed under")
	}
	// And the row records that the daemon chose the credential, which is what
	// distinguishes it from an agent that brought its own and must NOT be
	// reattachable by a guessable id.
	if !st.Agents["worker"].NonceMinted {
		t.Error("the agent's nonce is not marked as minted, so the session_id path " +
			"is relying on something other than the fact it is meant to key on")
	}
	_ = first
}
