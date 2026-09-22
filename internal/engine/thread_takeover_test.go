package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
// A fixed clock, because the fold is handed one and never reads one. The
// recovery this drives does not branch on the time; the argument exists so
// that core states the rule it enforces (internal/hygiene, the purity
// guard) instead of making an exception for a helper.
var reattachNow = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

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
			}, reattachNow)
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
		}, reattachNow); res != nil {
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
		}, reattachNow)
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
	ok, _ := e.mayClaimSession(sid, tok, "", "")
	return ok
}

// A synthetic session id on another machine is not this machine's to take.
//
// `host-<ppid>` repeats across computers. The alias claim asked who holds
// the id anywhere on the board, and a dormant holder yields: so a fresh
// register on machine B stating host-12345 took the binding from a dormant
// agent on machine A with the same id, and A's lifecycle hooks and write
// guard resolved to nobody from then on. The host-scoped hook lookups of
// earlier rounds were tested against the fold directly and never went
// through this ingress. The holder that matters is the one on the caller's
// machine. Round twenty-one of the pre-release review.
func TestASessionTakeoverStaysOnItsOwnMachine(t *testing.T) {
	const synthetic = "host-12345"
	st := core.NewState("hub-node", core.DefaultLimits())
	st.Agents["old"] = &core.Agent{
		ID: "old", Name: "old", Status: core.StatusDormant, Nonce: "n-old", NonceMinted: true,
		SessionID: synthetic, Token: "tok-old",
		Agent: &core.AgentInfo{CWD: "/w/repo", HostID: "machine-a"}, Slots: map[string]core.Slot{},
	}
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	// What the bridge on machine-b sends: its synthetic id as the primary
	// and, on every call, as the alias in _meta.
	res, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "new", SessionID: synthetic, SessionAlias: synthetic,
		Agent: &core.AgentInfo{CWD: "/w/repo", HostID: "machine-b"},
	})
	if err != nil {
		t.Fatalf("register on machine-b: %v", err)
	}
	if res["agent_id"] == "old" {
		t.Fatalf("machine-b's register landed on machine-a's row: %v", res)
	}
	if l := e.state.AgentForHookOn(synthetic, "/w/repo", "machine-a"); l == nil || l.ID != "old" {
		t.Fatalf("after a register on machine-b, machine-a's hooks for %s resolve to %v, want old: "+
			"the binding was taken across machines", synthetic, l)
	}
	if l := e.state.AgentForHookOn(synthetic, "/w/repo", "machine-b"); l == nil || l.ID == "old" {
		t.Fatalf("machine-b's hooks for %s resolve to %v, want the new row", synthetic, l)
	}
}

// A takeover on one machine leaves the other machine's binding alone even
// when both have a dormant holder of the same synthetic id.
//
// Round twenty-one scoped the ingress lookup to the caller's machine, so
// the holder that YIELDS is chosen here. The fold's drop was never
// scoped: it visits every row and takes the id from any that is not
// active, so a register on machine A that legitimately took A's dormant
// holder also stripped B's. B's hooks and guard then resolved to nobody,
// which is the exact failure round twenty-one fixed, reached through the
// other half. The round-twenty-one test misses it because its registering
// machine has no holder of its own, so the drop returns early. Round
// forty of the pre-release review.
func TestATakeoverDoesNotStripTheOtherMachinesDormantHolder(t *testing.T) {
	const synthetic = "host-12345"
	st := core.NewState("hub-node", core.DefaultLimits())
	dormant := func(id, host string) *core.Agent {
		return &core.Agent{
			ID: id, Name: id, Status: core.StatusDormant, Nonce: "n-" + id, NonceMinted: true,
			SessionID: synthetic, Token: "tok-" + id,
			Agent: &core.AgentInfo{CWD: "/w/repo", HostID: host}, Slots: map[string]core.Slot{},
		}
	}
	st.Agents["on-a"] = dormant("on-a", "machine-a")
	st.Agents["on-b"] = dormant("on-b", "machine-b")
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	// A new session on machine-a, stating the synthetic id its bridge
	// derives from the harness pid: it takes machine-a's dormant row.
	res, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "fresh", SessionID: synthetic, SessionAlias: synthetic,
		Agent: &core.AgentInfo{CWD: "/w/repo", HostID: "machine-a"},
	})
	if err != nil {
		t.Fatalf("register on machine-a: %v", err)
	}
	fresh, _ := res["agent_id"].(string)

	if l := e.state.AgentForHookOn(synthetic, "/w/repo", "machine-b"); l == nil || l.ID != "on-b" {
		t.Fatalf("machine-b's hooks for %s resolve to %v, want on-b: a takeover on machine-a "+
			"took a binding on another computer", synthetic, l)
	}
	if l := e.state.AgentForHookOn(synthetic, "/w/repo", "machine-a"); l == nil || l.ID != fresh {
		t.Fatalf("machine-a's hooks for %s resolve to %v, want the new row: the takeover it "+
			"was entitled to did not happen", synthetic, l)
	}
}

// An agent that resumes on a different machine takes the thread there,
// and the holder it took it from loses it.
//
// The drop asks which machine the take is being made from, and read it
// off the ROW, which on a resume still says where that agent was last
// time: the fold runs the drop before the activation's identity is
// merged. So an agent moving from machine A to machine B, taking a
// harness thread from a dormant holder on B, was authorised by the
// ingress on B (mayClaimSession, which asks hostOfOp) and then skipped
// B's holder as another computer's: two rows on B holding one thread,
// and hooks resolving to whichever. The taker's machine is the one this
// activation is on. Round forty-one of the pre-release review, on round
// forty's own fix.
func TestAResumeOnAnotherMachineTakesTheThreadThere(t *testing.T) {
	const thread = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	st := core.NewState("hub-node", core.DefaultLimits())
	// The row that moves: last seen on machine-a, dormant, with a nonce.
	st.Agents["mover"] = &core.Agent{
		ID: "mover", Name: "mover", Status: core.StatusDormant, Nonce: "n-mover", NonceMinted: true,
		Token: "tok-mover", Agent: &core.AgentInfo{CWD: "/w/repo", HostID: "machine-a"},
		Slots: map[string]core.Slot{},
	}
	st.Nonces = map[string]string{"n-mover": "mover"}
	// A dormant holder of the same thread, on the machine it moves to.
	st.Agents["on-b"] = &core.Agent{
		ID: "on-b", Name: "on-b", Status: core.StatusDormant, Nonce: "n-b", NonceMinted: true,
		SessionID: "host-4242", SessionAliases: []string{thread}, Token: "tok-b",
		Agent: &core.AgentInfo{CWD: "/w/repo", HostID: "machine-b"}, Slots: map[string]core.Slot{},
	}
	st.Nonces["n-b"] = "on-b"
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpResume, Nonce: "n-mover", ResumeID: "r1", SessionAlias: thread,
		Agent: &core.AgentInfo{CWD: "/w/repo", HostID: "machine-b"},
	}); err != nil {
		t.Fatalf("resume on machine-b: %v", err)
	}

	if l := e.state.AgentForHookOn(thread, "/w/repo", "machine-b"); l == nil || l.ID != "mover" {
		t.Fatalf("machine-b's hooks for the thread resolve to %v, want the resumed row: two "+
			"rows there hold one thread and the drop skipped the one it was taken from", l)
	}
	if e.state.Agents["on-b"].HoldsSession(thread) {
		t.Fatal("the holder the thread was taken from still holds it: two stated holders of " +
			"one id is a coin flip on every hook")
	}
}

// A coordinator releasing one machine's claim does not take another
// machine's protection off.
//
// An absolute path names a different file on each computer, so two agents
// holding /workspace/repo/file.go on two machines is not a collision and
// both claims are real. force_release matched on the path alone and
// dropped whichever was first in the slice, so a coordinator unsticking
// machine B could remove machine A's protection and leave B's claim
// exactly where it was: the resource the coordinator was protecting is
// now unprotected, and the one it meant to free is still held. Round
// forty-four of the pre-release review.
func TestForceReleaseDoesNotTakeAnotherMachinesClaim(t *testing.T) {
	const path = "/workspace/repo/file.go"
	st := core.NewState("hub-node", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	reg := func(name, host string) string {
		t.Helper()
		res, err := e.Do(ctx, &core.Op{
			Kind: core.OpRegister, Name: name, SessionID: "session-" + name,
			Agent: &core.AgentInfo{CWD: "/workspace/repo", HostID: host},
		})
		if err != nil {
			t.Fatalf("setup: register %s: %v", name, err)
		}
		tok, _ := res["token"].(string)
		if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok}); err != nil {
			t.Fatalf("setup: ack %s: %v", name, err)
		}
		return tok
	}
	onA, onB, coord := reg("on-a", "machine-a"), reg("on-b", "machine-b"), reg("coord", "hub-node")
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpGrantRole, To: "coord", Mode: core.RoleCoordinator}); err != nil {
		t.Fatalf("setup: grant: %v", err)
	}
	for _, tok := range []string{onA, onB} {
		res, err := e.Do(ctx, &core.Op{Kind: core.OpClaim, Token: tok, Path: path, Mode: core.ClaimExclusive})
		if err != nil {
			t.Fatalf("setup: claim: %v", err)
		}
		if res["granted"] != true {
			t.Fatalf("setup: a claim on another machine was refused: %v", res)
		}
	}

	// Naming no holder is ambiguous, and a guess here unprotects a file
	// somebody is writing: it is refused, and the refusal says who holds it.
	_, err := e.Do(ctx, &core.Op{Kind: core.OpForceRelease, Token: coord, Path: path})
	if err == nil {
		t.Fatal("force_release with two holders on two machines picked one: whichever it " +
			"chose, a resource somebody is writing is now unprotected")
	}
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Code != "E_AMBIGUOUS_CLAIM" {
		t.Fatalf("refused with %v, want E_AMBIGUOUS_CLAIM naming the holders", err)
	}
	if !strings.Contains(ce.Msg, "on-a") || !strings.Contains(ce.Msg, "on-b") {
		t.Errorf("the refusal does not name both holders: %q", ce.Msg)
	}

	// Named, it releases exactly that one.
	res, err := e.Do(ctx, &core.Op{Kind: core.OpForceRelease, Token: coord, Path: path, To: "on-b"})
	if err != nil {
		t.Fatalf("force_release naming the holder: %v", err)
	}
	if res["was_held_by"] != "on-b" {
		t.Fatalf("force_release released %v, want on-b", res["was_held_by"])
	}
	var left []string
	onLoop(t, ctx, e, func(s *core.State) {
		for _, c := range s.Claims {
			if c.Path == path {
				left = append(left, c.Agent)
			}
		}
	})
	if len(left) != 1 || left[0] != "on-a" {
		t.Fatalf("claims left on %s: %v, want on-a's alone", path, left)
	}
}
