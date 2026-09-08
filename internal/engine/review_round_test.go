package engine

import (
	"context"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// F4: taking a thread from a dormant holder takes it AWAY from that holder.
//
// mayClaimSession permitted the takeover and the bind added the alias to the
// new agent, and nothing removed it from the dormant one. Two stated holders of
// one id, and AgentForHook answered from map order: a hook could resolve to the
// abandoned mailbox instead of the agent in front of it. The register path
// already recorded this; every other op that binds an alias did not. Found by
// the pre-release review.
func TestATakenAliasLeavesTheDormantHolder(t *testing.T) {
	const thread = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	st := core.NewState("test", core.DefaultLimits())
	// SETUP ON THE STATE BEFORE THE LOOP EXISTS. The engine is single-writer and
	// its loop reads state on an idle tick; writing to st from the test while
	// go e.Run is live is a data race, and the race detector said so on the
	// first draft of this file.
	t0 := time.Now()
	for _, op := range []*core.Op{
		{
			Kind: core.OpRegister, Name: "old", NewToken: "tok-old", AgentKind: core.KindPersistent,
			Nonce: "n-old", SessionID: thread, V7Semantics: true,
		},
		{
			Kind: core.OpRegister, Name: "live", NewToken: "tok-live", AgentKind: core.KindPersistent,
			Nonce: "n-live", V7Semantics: true,
		},
	} {
		if _, _, err := st.Apply(op, t0); err != nil {
			t.Fatal("setup:", err)
		}
	}
	st.Agents["old"].Status = core.StatusDormant
	const tok = "tok-live"
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	// The live agent names the thread on an ordinary call, as codex does.
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok, SessionAlias: thread}); err != nil {
		t.Fatal(err)
	}
	if !st.Agents["live"].HoldsSessionForTest(thread) {
		t.Fatal("the live agent did not take the thread at all, so this proves nothing")
	}
	if st.Agents["old"].HoldsSessionForTest(thread) {
		t.Error("the dormant holder still holds the thread after it was taken: two stated " +
			"holders, and a hook for this thread resolves by map order")
	}
	if got := st.AgentBySession(thread); got == nil || got.ID != "live" {
		t.Errorf("the thread resolves to %v, not the live agent", got)
	}
}

// And while two stated holders exist, the lookup is not a coin flip.
func TestTwoStatedHoldersResolveToTheLiveOneEveryTime(t *testing.T) {
	const thread = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	seen := map[string]int{}
	for range 40 {
		st := core.NewState("test", core.DefaultLimits())
		st.Agents["a-dormant"] = &core.Agent{
			ID: "a-dormant", Name: "a", Status: core.StatusDormant,
			SessionID: thread, Slots: map[string]core.Slot{},
		}
		st.Agents["b-live"] = &core.Agent{
			ID: "b-live", Name: "b", Status: core.StatusActive,
			SessionID: thread, Slots: map[string]core.Slot{},
		}
		seen[st.AgentBySession(thread).ID]++
	}
	if len(seen) != 1 || seen["b-live"] == 0 {
		t.Errorf("one thread resolved to different agents across identical lookups: %v", seen)
	}
}

// F5: an agent that lost its context is recovered at ingress, not refused as a thief.
//
// The ingress guard kept its own copy of the fold's reattach rule and fell
// behind it. The fold recovers rows with a minted nonce; the guard required an
// empty one, so a default registration that lost its token and came back by
// name and session, exactly as instructed, got E_SESSION_TAKEN before the fold
// was consulted. Found by the pre-release review.
func TestAnActiveAgentWithAMintedNonceIsRecoveredBySession(t *testing.T) {
	const thread = "01a0696b-8446-7821-a992-9dc7f6a43a25"
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "worker", SessionID: thread}); err != nil {
		t.Fatal("setup:", err)
	}
	if !st.Agents["worker"].NonceMinted {
		t.Fatal("setup: the nonce was not minted, so this is not the case under test")
	}
	back, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "worker", SessionID: thread})
	if err != nil {
		t.Fatalf("refused at ingress: %v. The fold would have recovered this agent; the "+
			"guard in front of it still applied the old rule", err)
	}
	if id, _ := back["agent_id"].(string); id != "worker" || back["reattached"] != true {
		t.Errorf("came back as %v (reattached=%v), not as itself", id, back["reattached"])
	}
}

// F6: mail an heir was GIVEN is readable, unlike mail a reused id inherited.
//
// read_mail treated every message older than the reader's creation as
// inherited. An heir adopting an abandoned mailbox receives older messages on
// purpose, by an authorised op: inbox listed them, the wake nudge pointed at
// read_mail, and read_mail said E_NO_MESSAGE. Found by the pre-release review.
func TestAnHeirCanReadTheMailItAdopted(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	t0 := time.Now()
	mk := func(name, tok string) {
		if _, _, err := st.Apply(&core.Op{
			Kind: core.OpRegister, Name: name, NewToken: tok,
			AgentKind: core.KindPersistent, Nonce: "n-" + name, V7Semantics: true,
		}, t0); err != nil {
			t.Fatal("setup:", err)
		}
	}
	mk("lost", "tok-lost")
	mk("asker", "tok-asker")
	r, _, err := st.Apply(&core.Op{
		Kind: core.OpSendMessage, Token: "tok-asker", To: "lost",
		MsgType: core.MsgQuestion, Body: "for whoever holds this seat", V7Semantics: true,
	}, t0)
	if err != nil {
		t.Fatal("setup:", err)
	}
	serial := r["msg_serial"].(uint64)
	st.Agents["lost"].Status = core.StatusDormant
	mk("heir", "tok-heir")
	if st.Agents["heir"].CreatedSerial <= serial {
		t.Fatal("setup: the heir is not younger than the message, so nothing is inherited")
	}
	if _, _, err := st.Apply(&core.Op{
		Kind: core.OpAdoptAgent, Token: "tok-heir", To: "lost",
		AdoptAuthorised: true, V7Semantics: true,
	}, t0); err != nil {
		t.Fatal("setup:", err)
	}
	// The loop starts only now, after every direct write to the state.
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	res, err := e.GetMessage(ctx, "tok-heir", serial)
	if err != nil {
		t.Fatal(err)
	}
	if res["error"] != nil {
		t.Errorf("the heir was refused the message it was just given: %v", res["error"])
	}
}
