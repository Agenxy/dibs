package core

import (
	"testing"
	"time"
)

// A RE-REGISTER WHILE LIVE IS THE ONE PATH THAT DROPPED THE IDENTITY.
//
// Four paths carry an AgentInfo to a row. A fresh register takes it, a reattach
// after dormancy takes it (applyRegister's nonce branch), update merges the
// self-reported half. The fourth is a same-nonce register while the row is
// still active inside one TTL, and resumeWork decided whether that was a real
// change from sessions, pid and nonce alone: an identity that differed in every
// field was not a reason, so the whole of it was dropped and the caller told
// `resumed: true`. Issue #78 reported it for cwd from a live board.
//
// It bites harder now that the identity carries a SERVER-DERIVED field. host_id
// is stamped at ingress and cannot be set by update, so for an agent already on
// the board the live re-register was the only way to get one, and it was the
// path that threw it away. Measured on this board the hour the field shipped:
// two re-registers minutes apart, both `resumed: true`, and the row never
// gained a host id.
//
// The rule from resumeWork's own list applies: a re-register that states a
// different identity is not a retried call whose answer was lost. It is a
// change, it is ledgered as one, and the row takes what the server derived.
func TestARegisterWhileLiveTakesTheIdentityItCarries(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)

	mustApply(t, s, &Op{
		Kind: OpRegister, Name: "worker", NewToken: "tok-1", Nonce: "n-keep",
		Agent:       &AgentInfo{Harness: "Codex", CWD: "/old"},
		V7Semantics: true, TakeIdentity: true,
	}, now)
	id := s.Nonces["n-keep"]
	before := s.Serial

	// Same nonce, same session, same process, a minute later: the shape that
	// used to short-circuit as a lost-response retry. The identity has moved
	// and, for the first time, names the machine.
	res := mustApply(t, s, &Op{
		Kind: OpRegister, Name: "worker", NewToken: "tok-2", Nonce: "n-keep",
		Agent:       &AgentInfo{Harness: "Codex", CWD: "/new", HostID: "51c33463"},
		V7Semantics: true, TakeIdentity: true,
	}, now.Add(time.Minute))

	l := s.Agents[id]
	if l.Agent == nil || l.Agent.HostID != "51c33463" {
		t.Fatalf("the row never gained the host id the server derived for it. This "+
			"is the only path an already-registered agent has to one, and it "+
			"reported success with the old identity: %+v", l.Agent)
	}
	if l.Agent.CWD != "/new" {
		t.Errorf("the corrected cwd was dropped, which is issue #78 exactly: %+v", l.Agent)
	}
	// A CHANGE IS LEDGERED. An op that moves replayable state without advancing
	// the serial is the invariant this repository guards hardest.
	if s.Serial == before {
		t.Error("the identity moved on the row and the serial did not: replay would " +
			"rebuild a board without it")
	}
	if res["resumed"] != true {
		t.Errorf("a same-nonce register of a live agent is still a resume: %v", res)
	}
}

// And the SAME identity, stated twice, is still the retry it always was.
//
// The lost-response case is real and the reason this path exists: an identical
// registration arriving twice is one call whose answer the client never saw,
// and it must return the original result, keep the token, and touch nothing.
// Widening "changed" to cover identity must not swallow that.
func TestAnIdenticalRegisterWhileLiveIsStillARetry(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)

	info := &AgentInfo{Harness: "Codex", CWD: "/here", HostID: "51c33463"}
	first := mustApply(t, s, &Op{
		Kind: OpRegister, Name: "worker", NewToken: "tok-1", Nonce: "n-keep",
		Agent: info, V7Semantics: true, TakeIdentity: true,
	}, now)
	before := s.Serial

	again := mustApply(t, s, &Op{
		Kind: OpRegister, Name: "worker", NewToken: "tok-2", Nonce: "n-keep",
		Agent:       &AgentInfo{Harness: "Codex", CWD: "/here", HostID: "51c33463"},
		V7Semantics: true, TakeIdentity: true,
	}, now.Add(time.Second))

	if again["token"] != first["token"] {
		t.Errorf("an identical retry rotated the token from %v to %v, revoking the "+
			"credential the caller is already using", first["token"], again["token"])
	}
	if s.Serial != before {
		t.Error("an identical retry advanced the serial: a no-op was ledgered")
	}
}

// A LEDGER FROM BEFORE THE FLAG REPLAYS TO WHAT IT BUILT.
//
// takeActivation already applies the identity when the SESSION moves, so that
// shape is not what the gate protects. The shape that is: a same-nonce register
// that was a change for some other reason, a restated pid being the ordinary
// one, and so was ledgered, and dropped its identity on the way to disk. Those
// ops are on disk today. Applying the identity to them on replay would rebuild
// a cwd, and so a checkout root, and so every claim's portable name, that the
// daemon never held live: state != fold(ledger) on the field the claim rule
// reads.
//
// So the decision is recorded on the op, as RestoreNonce and KeepArchivedNonce
// are. An op without it keeps the semantics it was written under.
func TestARegisterWrittenBeforeTheFlagStillDropsTheIdentity(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)

	mustApply(t, s, &Op{
		Kind: OpRegister, Name: "worker", NewToken: "tok-1", Nonce: "n-keep",
		SessionID: "sess-1", PID: 41,
		Agent: &AgentInfo{Harness: "Codex", CWD: "/old"}, V7Semantics: true,
	}, now)
	id := s.Nonces["n-keep"]
	before := s.Serial

	// Historical shape: V7Semantics as every op since v0.0.7 carries, the SAME
	// session, a new pid so it is a change and is ledgered, no TakeIdentity.
	mustApply(t, s, &Op{
		Kind: OpRegister, Name: "worker", NewToken: "tok-2", Nonce: "n-keep",
		SessionID: "sess-1", PID: 42,
		Agent:       &AgentInfo{Harness: "Codex", CWD: "/new", HostID: "51c33463"},
		V7Semantics: true,
	}, now.Add(time.Minute))

	if s.Serial == before {
		t.Fatal("setup: the pid change was not ledgered, so this is not the shape " +
			"the gate exists for")
	}
	l := s.Agents[id]
	if l.Agent.CWD != "/old" || l.Agent.HostID != "" {
		t.Fatalf("an unflagged register applied its identity on replay: a ledger "+
			"written by v0.0.7 now rebuilds a row its own daemon never held, "+
			"%+v", l.Agent)
	}
}

// update carries the machine too, so an agent that never re-registers is not
// stranded without one. Server-derived, like the location group, and taken
// only when stated: every update op on disk today carries none.
func TestUpdateTakesTheHostTheServerDerived(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)

	reg(t, s, "worker", "tok-1", now)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "tok-1"}, now)
	mustApply(t, s, &Op{
		Kind: OpUpdate, Token: "tok-1", Agent: &AgentInfo{HostID: "51c33463"},
	}, now.Add(time.Second))

	if got := s.Agents["worker"].Agent.HostID; got != "51c33463" {
		t.Fatalf("update dropped the host id: got %q", got)
	}
	// An update that states none leaves it alone rather than blanking it.
	mustApply(t, s, &Op{
		Kind: OpUpdate, Token: "tok-1", Agent: &AgentInfo{Title: "renamed"},
	}, now.Add(2*time.Second))
	if got := s.Agents["worker"].Agent.HostID; got != "51c33463" {
		t.Errorf("an update that said nothing about the machine cleared it: %q", got)
	}
}
