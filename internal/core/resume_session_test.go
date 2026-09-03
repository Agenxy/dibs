package core

import (
	"testing"
	"time"
)

// A resuming agent's thread id is bound, so it can still be woken afterwards.
//
// The `resumed` branch of register was written as a response-loss retry: same
// nonce twice inside one TTL means the client never saw the first answer, so
// return it again and change nothing. That is right for a retry and wrong for
// the other traffic that lands there. An active agent re-registering at the
// start of an activation, which is what dibs://skills instructs, also comes
// back `resumed`, and it may be doing so from a session the board has never
// seen. Codex sends `threadId` in `_meta` on every call and that id is what
// `codex exec resume` takes: dropping it here left the agent unwakeable unless
// it happened to make some other call afterwards.
//
// Measured on a live board before this was written: fifteen of twenty-nine
// persistent agents had a wake command for their harness and no thread for it
// to name, one of which had registered that same morning.
func TestAResumingAgentKeepsTheThreadItNames(t *testing.T) {
	s := NewState("test", DefaultLimits())
	t0 := time.Now()
	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "worker", AgentKind: KindPersistent, Nonce: "n1",
		NewToken: "tok-1", V7Semantics: true,
	}, t0); err != nil {
		t.Fatal("setup:", err)
	}
	before := s.Serial

	const thread = "01a0696b-8446-7821-a992-9dc7f6a43a25"
	res, evs, err := s.Apply(&Op{
		Kind: OpRegister, Name: "worker", Nonce: "n1", NewToken: "tok-2",
		SessionAlias: thread, V7Semantics: true,
	}, t0.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if res["resumed"] != true {
		t.Fatalf("this test is not exercising the resume branch: %v", res)
	}
	if !s.Agents["worker"].holdsSession(thread) {
		t.Fatal("the resumed agent did not keep the thread id it named, so nothing " +
			"can resume it: `codex exec resume` and `claude --resume` both need " +
			"something to name, and this was the call that offered one")
	}
	// An op that changed replayable state is ledgered, and the engine appends
	// exactly when the serial moved (SPEC §2). Binding without advancing it
	// would lose the binding on the next restart.
	if s.Serial == before {
		t.Error("the serial did not advance, so the engine will not ledger this " +
			"and the binding dies with the daemon")
	}
	if len(evs) == 0 {
		t.Error("no event, so nothing downstream learns the agent became reachable")
	}
}

// A genuine response-loss retry still changes nothing.
//
// The repair above must not turn every duplicate register into a ledger write.
// A client repeating a call it never saw the answer to carries the same alias
// it carried the first time, so the binding is not new and there is nothing to
// record.
func TestARepeatedRegisterIsStillANoOp(t *testing.T) {
	s := NewState("test", DefaultLimits())
	t0 := time.Now()
	const thread = "01a0696b-8446-7821-a992-9dc7f6a43a25"
	op := func(tok string) *Op {
		return &Op{
			Kind: OpRegister, Name: "worker", AgentKind: KindPersistent, Nonce: "n1",
			NewToken: tok, SessionAlias: thread, V7Semantics: true,
		}
	}
	if _, _, err := s.Apply(op("tok-1"), t0); err != nil {
		t.Fatal("setup:", err)
	}
	before := s.Serial
	if _, evs, err := s.Apply(op("tok-2"), t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	} else if len(evs) != 0 {
		t.Errorf("a repeated register emitted %d event(s): a retry is not a change, "+
			"and ledgering one would grow the log on every duplicate call", len(evs))
	}
	if s.Serial != before {
		t.Errorf("the serial moved on a pure retry (%d -> %d), which breaks the rule "+
			"that an op is ledgered iff it changed replayable state", before, s.Serial)
	}
}

// Old ops keep the semantics they were written under.
//
// Without the V7Semantics gate, replaying a v0.0.6 ledger would bind here and
// advance the serial where the original fold did not, so every serial after it
// would disagree with what the ledger records: the daemon reports serial gaps
// and, in the worst case, refuses its own history.
func TestAPreV7ResumeDoesNotBindOnReplay(t *testing.T) {
	s := NewState("test", DefaultLimits())
	t0 := time.Now()
	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "worker", AgentKind: KindPersistent, Nonce: "n1",
		NewToken: "tok-1",
	}, t0); err != nil {
		t.Fatal("setup:", err)
	}
	before := s.Serial
	if _, evs, err := s.Apply(&Op{
		Kind: OpRegister, Name: "worker", Nonce: "n1", NewToken: "tok-2",
		SessionAlias: "01a0696b-8446-7821-a992-9dc7f6a43a25", // no V7Semantics
	}, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	} else if len(evs) != 0 || s.Serial != before {
		t.Errorf("an op written before v0.0.7 was folded with today's rules: "+
			"%d event(s), serial %d -> %d. Replay of an existing ledger now "+
			"diverges from what that ledger records", len(evs), before, s.Serial)
	}
}
