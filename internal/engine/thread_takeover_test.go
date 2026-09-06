package engine

import (
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A live agent takes a thread from a holder that has stopped answering.
//
// A dormant agent's session ended with its process, so it cannot be occupying
// the thread it still holds. While it did, both ends of the wake path broke at
// once: a wake for the dormant row started the thread and reached whoever was
// running in it now, who read their own mailbox, found it empty and truthfully
// reported no mail; and the agent that actually WAS that session, refused its
// own id, held no thread and so could never be woken by anything at all.
//
// Measured on this project's own board, with `codex-root-2` dormant for three
// weeks and a live agent in the thread it owned.
//
// This is the second implementation of one rule.
// refuseStealingAnotherThreadsSession learned it first and alone, and this
// function went on refusing for months because it is reached by a different
// call four hundred lines away. Both are tested here on purpose, against one
// fixture, so the next person to change either finds the other.
func TestALiveAgentTakesAThreadFromADormantHolder(t *testing.T) {
	const thread = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"

	newBoard := func(t *testing.T, holderStatus core.AgentStatus) *Engine {
		t.Helper()
		st := core.NewState("test", core.DefaultLimits())
		st.Agents["old"] = &core.Agent{
			ID: "old", Name: "old", Status: holderStatus, Nonce: "n-old",
			SessionID: thread, Token: "tok-old",
			Agent: &core.AgentInfo{CWD: "/work"}, Slots: map[string]core.Slot{},
		}
		st.Agents["live"] = &core.Agent{
			ID: "live", Name: "live", Status: core.StatusActive, Token: "tok-live",
			Agent: &core.AgentInfo{CWD: "/work"}, Slots: map[string]core.Slot{},
		}
		return New(st, &memLedger{}, deadProber{})
	}

	t.Run("a dormant holder yields", func(t *testing.T) {
		e := newBoard(t, core.StatusDormant)
		if !mayClaim(e, thread, "tok-live") {
			t.Error("a live agent was refused the thread it is running in, by a row " +
				"that went dormant. It now holds no thread at all, so nothing can " +
				"wake it, and wakes for the dormant row reach the wrong mailbox")
		}
	})

	t.Run("an active holder keeps it", func(t *testing.T) {
		e := newBoard(t, core.StatusActive)
		if mayClaim(e, thread, "tok-live") {
			t.Error("a thread was taken from an ACTIVE holder. Two live agents " +
				"claiming one session is a real conflict, and moving the binding " +
				"would redirect a working agent's wake delivery onto another")
		}
	})

	// And the op-level guard has to agree, or one path allows what the other
	// refuses and the disagreement is invisible until a wake goes missing.
	t.Run("the register guard agrees", func(t *testing.T) {
		e := newBoard(t, core.StatusDormant)
		op := &core.Op{Kind: core.OpRegister, Name: "live", SessionID: thread, Token: "tok-live"}
		if err := e.refuseStealingAnotherThreadsSession(op); err != nil {
			t.Fatalf("the two guards disagree: mayClaimSession allows this and the "+
				"register guard refuses it: %v", err)
		}
		if op.SessionTakenFrom != "old" {
			t.Errorf("the takeover was not recorded on the op (got %q), so replay "+
				"cannot reproduce which row gave the thread up", op.SessionTakenFrom)
		}
	})
}

// An agent is recovered by ANY id it answers to, including a parked one.
//
// Two gaps, found together while trying to retire a test row and making two
// more instead of one fewer.
//
// The reattach path matched an agent's PRIMARY session id only. An agent goes
// by several: the bridge derives one, and a harness that names its own thread
// contributes another as an alias. Codex sends `threadId` in `_meta` on every
// call, so for a codex agent the identifier that actually identifies it is
// almost always the alias, and it was the one id that would not work.
//
// And the status pair was active-or-stale. `stale` is where an EPHEMERAL agent
// lands; `dormant` is the persistent equivalent and was absent. Harmless while
// persistent agents were rare and held operator-chosen nonces; not harmless
// once persistent became the default, because the common case is now an agent
// that parked, holds a nonce it was given rather than chose, and can offer
// nothing but its thread.
func TestAnAgentIsRecoveredByAnyIDItAnswersTo(t *testing.T) {
	const thread = "01a0696b-9999-7821-a992-9dc7f6a43a11"

	mk := func(t *testing.T, status core.AgentStatus, minted bool) *core.State {
		t.Helper()
		st := core.NewState("test", core.DefaultLimits())
		l := &core.Agent{
			ID: "worker", Name: "worker", Status: status,
			SessionID: "bridge-1234", Nonce: "n-worker", NonceMinted: minted,
			Agent: &core.AgentInfo{CWD: "/work"}, Slots: map[string]core.Slot{},
		}
		l.SessionAliases = []string{thread} // the harness's own name for it
		st.Agents["worker"] = l
		return st
	}

	for _, c := range []struct {
		name   string
		status core.AgentStatus
	}{
		{"parked", core.StatusDormant},
		{"lapsed", core.StatusStale},
		{"working", core.StatusActive},
	} {
		t.Run(c.name+" recovers by its thread id", func(t *testing.T) {
			st := mk(t, c.status, true)
			res, _ := st.ReattachBySessionIDForTest(&core.Op{
				Kind: core.OpRegister, Name: "worker", SessionID: thread, V7Semantics: true,
				NewToken: "tok-new",
			})
			if res == nil {
				t.Fatalf("a %s agent could not be recovered by the one id its harness "+
					"gives it, so re-registering forks a sibling that cannot read its "+
					"own mail", c.name)
			}
			if id, _ := res["agent_id"].(string); id != "worker" {
				t.Errorf("recovered %q instead of the agent itself", id)
			}
		})
	}

	// And a chosen credential is still not reachable by an id somebody guesses.
	t.Run("an agent that chose its own nonce is not", func(t *testing.T) {
		st := mk(t, core.StatusDormant, false)
		if res, _ := st.ReattachBySessionIDForTest(&core.Op{
			Kind: core.OpRegister, Name: "worker", SessionID: thread, V7Semantics: true, NewToken: "tok-new",
		}); res != nil {
			t.Error("an agent holding a nonce IT chose was reattached by a session id. " +
				"A real secret must beat a guessable identifier, which is the whole " +
				"reason this branch checks the credential at all")
		}
	})
}

// Two rows that both match one reattach resolve the same way every time.
//
// A name that comes back is suffixed in the ID and keeps the NAME, so
// `worker` and `worker-2` are both named "worker", and both can hold one
// thread: the first as an alias, the second as the id it registered under. The
// loop that picked between them ranged over a Go map and took the first hit,
// which is randomised. A coin flip inside the fold breaks state == fold(ledger):
// one ledger replays to different boards on different runs, and nothing reports
// it, because each run is internally consistent.
//
// Observed on this board within a minute of widening the match to aliases and
// dormant rows, which turned a collision from exotic into ordinary.
//
// Run repeatedly ON PURPOSE. Map order is randomised per iteration, so a single
// pass proves nothing at all: it is exactly the shape of test that passes
// against the bug it was written for.
func TestOneSessionIDAlwaysRecoversTheSameRow(t *testing.T) {
	const thread = "01a0696b-8446-7821-a992-9dc7f6a43a25"

	board := func() *core.State {
		st := core.NewState("test", core.DefaultLimits())
		// Holds the thread only as an alias, and has stopped answering.
		old := &core.Agent{
			ID: "worker", Name: "worker", Status: core.StatusDormant,
			SessionID: "bridge-1", Nonce: "n1", NonceMinted: true,
			Slots: map[string]core.Slot{},
		}
		old.SessionAliases = []string{thread}
		// Registered UNDER the thread, and still working.
		st.Agents["worker"] = old
		st.Agents["worker-2"] = &core.Agent{
			ID: "worker-2", Name: "worker", Status: core.StatusActive,
			SessionID: thread, Nonce: "n2", NonceMinted: true,
			Slots: map[string]core.Slot{},
		}
		return st
	}

	seen := map[string]int{}
	for range 50 {
		res, _ := board().ReattachBySessionIDForTest(&core.Op{
			Kind: core.OpRegister, Name: "worker", SessionID: thread, V7Semantics: true, NewToken: "tok",
		})
		if res == nil {
			t.Fatal("no row was recovered at all, so this proves nothing about which")
		}
		id, _ := res["agent_id"].(string)
		seen[id]++
	}
	if len(seen) != 1 {
		t.Fatalf("one session id recovered different rows across identical runs: %v.\n"+
			"Replaying a single ledger now reaches different boards, and nothing "+
			"reports it because each run is internally consistent", seen)
	}
	// And it is the row that registered under this id, not the one that merely
	// answers to it as well.
	if _, ok := seen["worker-2"]; !ok {
		t.Errorf("recovered %v. The row whose PRIMARY session id this is has the "+
			"stronger claim than one holding it as an alias", seen)
	}
}

// mayClaim is the verdict alone, for tests written when that was all it gave.
func mayClaim(e *Engine, sid, tok string) bool {
	ok, _ := e.mayClaimSession(sid, tok, "")
	return ok
}
