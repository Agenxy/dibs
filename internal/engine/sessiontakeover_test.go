package engine

import (
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A live session takes its own id back from a holder that has stopped answering.
//
// A session id names a HARNESS THREAD, and a thread has one occupant. The
// register path refused any id another agent held unless that agent was closed
// or archived, so a DORMANT row blocked the live session behind it forever: the
// rightful session was told "already held by <agent>" and pointed at
// register-with-your-nonce, which is a call only the OTHER agent can make. The
// documented remedy, update(release_session), needs that agent's own token. So
// there was no remedy the refused party could take at all.
//
// This is what made Dibs non-functional on its own board. Measured there: 29
// lifecycle hooks from working sessions, not one resolving to any agent, the
// claim guard allowing every edit and no mail ever injected, because the
// session that could have registered was refused its own id by a row that had
// been dormant for days.
//
// Nothing about mail moves with the id. The old row keeps its mailbox, its
// history and its recovery credential; only where a WAKE is delivered changes,
// and a dormant agent was not receiving those.
//
// The decision is tested rather than the wrapper: e.query on an engine with no
// loop running blocks forever instead of failing. See AGENTS.md.
func TestALiveSessionTakesItsIDFromADormantHolder(t *testing.T) {
	const sid = "19d67315-7718-491e-be3f-3864f577eeed"
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	now := t0Engine()

	if _, _, err := st.Apply(&core.Op{
		Kind: core.OpRegister, Name: "stale-holder", NewToken: "tok-stale",
		SessionID: sid, Nonce: "nonce-stale",
	}, now); err != nil {
		t.Fatal("setup:", err)
	}
	st.Agents["stale-holder"].Status = core.StatusDormant

	op := &core.Op{
		Kind: core.OpRegister, Name: "live-agent", NewToken: "tok-live",
		SessionID: sid, Nonce: "nonce-live",
	}
	if err := e.refuseStealingAnotherThreadsSession(op); err != nil {
		t.Fatalf("the session that owns %s was refused its own id: %v.\n"+
			"  Its hooks then resolve to nobody, the claim guard allows every edit, "+
			"and no mail is ever injected into it.", sid, err)
	}
	if op.SessionTakenFrom != "stale-holder" {
		t.Fatalf("the losing holder was not recorded on the op (%q), so replay "+
			"would rebuild a board where both rows hold the id", op.SessionTakenFrom)
	}
	if _, _, err := st.Apply(op, now); err != nil {
		t.Fatal("register:", err)
	}

	// Exactly one row answers to the thread, or which agent a hook reaches
	// depends on map order rather than on anything a reader can see.
	if holder := st.AgentBySession(sid); holder == nil || holder.ID != "live-agent" {
		t.Errorf("the thread resolves to %v, not the live agent", holder)
	}
	if st.Agents["stale-holder"].SessionID == sid {
		t.Error("the dormant row still holds the id: two stated holders, and " +
			"AgentBySession then picks between them by id order")
	}
	// It keeps everything that is actually its own.
	if old := st.Agents["stale-holder"]; old == nil || old.Nonce == "" {
		t.Error("taking the session id must not take the agent: its mailbox, " +
			"history and recovery credential stay with it")
	}
}

// An ACTIVE holder keeps its session, because that is a real conflict.
//
// Two live agents claiming one thread is not stale state, and taking it would
// redirect a working agent's wakes to somebody else. Without this the fix above
// is not a repair, it is a way to steal a wake stream.
func TestAnActiveHolderKeepsItsSession(t *testing.T) {
	const sid = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	now := t0Engine()

	if _, _, err := st.Apply(&core.Op{
		Kind: core.OpRegister, Name: "working", NewToken: "tok-w",
		SessionID: sid, Nonce: "nonce-w",
	}, now); err != nil {
		t.Fatal("setup:", err)
	}
	if st.Agents["working"].Status != core.StatusActive {
		t.Fatal("setup: the holder is not active, so this measures the other case")
	}

	op := &core.Op{
		Kind: core.OpRegister, Name: "interloper", NewToken: "tok-i",
		SessionID: sid, Nonce: "nonce-i",
	}
	if err := e.refuseStealingAnotherThreadsSession(op); err == nil {
		t.Error("an active agent lost its session id to another register")
	}
	if op.SessionTakenFrom != "" {
		t.Errorf("a refused claim still recorded a takeover: %q", op.SessionTakenFrom)
	}
}
