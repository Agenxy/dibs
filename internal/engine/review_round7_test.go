package engine

import (
	"context"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/peerwake"
)

// R7-1: a wake refused because the socket cache had not seen the session yet
// is retried after the cache can refresh, instead of being the final word.
func TestAWakeRefusedOnAStaleSocketCacheIsRetried(t *testing.T) {
	const sid = "ab2bdbe2-3bc9-4f7b-8a1f-a638093a6256"
	e := New(core.NewState("test", core.DefaultLimits()), &memLedger{}, deadProber{})
	e.state.Agents["cc"] = &core.Agent{
		ID: "cc", Name: "cc", Status: core.StatusActive, SessionID: sid,
		Agent: &core.AgentInfo{Harness: "Claude Code", CWD: "/work"},
		Slots: map[string]core.Slot{},
	}
	e.peers.mu.Lock()
	e.peers.at = time.Now() // fresh, and scanned before this session existed
	e.peers.live = map[string]peerwake.Session{}
	e.peers.mu.Unlock()
	if _, ok := e.wakeFor(e.state.Agents["cc"], core.MsgQuestion, core.Event{}); ok {
		t.Fatal("setup: the stale snapshot produced a plan, so there is no refusal to retry")
	}
	e.maybeWake(core.Event{
		Type: "message.sent", To: "cc", Agent: "asker",
		Data: map[string]any{"msg_type": core.MsgQuestion, "from": "asker"},
	})
	e.wakers.mu.Lock()
	armed := e.wakers.deferred["cc"] != nil
	if armed {
		e.wakers.deferred["cc"].Stop()
	}
	e.wakers.mu.Unlock()
	if !armed {
		t.Fatal("a question to a session the cache had not seen was refused with no retry: " +
			"the one wake attempt this mail gets was spent on a stale snapshot")
	}
}

// R7-3, from the claimant's side: once the holder has bound the id
// explicitly, a live claim from another agent is refused.
func TestAStatedSessionCannotBeClaimedFromAnActiveHolder(t *testing.T) {
	const thread = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	for _, o := range []*core.Op{
		{Kind: core.OpRegister, Name: "a", NewToken: "tok-a", AgentKind: core.KindPersistent, Nonce: "na", SessionAlias: thread, SessionGuessed: true, V7Semantics: true},
		{Kind: core.OpRegister, Name: "b", NewToken: "tok-b", AgentKind: core.KindPersistent, Nonce: "nb", V7Semantics: true},
	} {
		if _, _, err := st.Apply(o, t0Engine()); err != nil {
			t.Fatal("setup:", err)
		}
	}
	if ok, _ := e.mayClaimSession(thread, "tok-b", ""); !ok {
		t.Fatal("setup: a guessed binding on an active holder was not claimable, so the " +
			"refusal below proves nothing")
	}
	if _, _, err := st.Apply(&core.Op{Kind: core.OpBindSession, Token: "tok-a", SessionID: thread, V7Semantics: true}, t0Engine()); err != nil {
		t.Fatal(err)
	}
	if ok, from := e.mayClaimSession(thread, "tok-b", ""); ok {
		t.Errorf("b may still claim the id a bound explicitly (taken from %q): the explicit "+
			"bind gave a no protection", from)
	}
}

// R7-5: register returns the fingerprint every role-pinning instruction says it does.
func TestRegisterReturnsTheFingerprintTheInstructionsPromise(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	res, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "pinned", NewToken: "tok-p",
		AgentKind: core.KindPersistent, Nonce: "n-pinned-0123456789abcdef",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := RolePinFingerprint("n-pinned-0123456789abcdef")
	if got, _ := res["fingerprint"].(string); got != want {
		t.Errorf("register returned fingerprint %q, want %q: the README, the configuration "+
			"guide and the daemon's own refusal all say to paste the value register returns",
			got, want)
	}
}
