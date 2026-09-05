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
		if !e.mayClaimSession(thread, "tok-live") {
			t.Error("a live agent was refused the thread it is running in, by a row " +
				"that went dormant. It now holds no thread at all, so nothing can " +
				"wake it, and wakes for the dormant row reach the wrong mailbox")
		}
	})

	t.Run("an active holder keeps it", func(t *testing.T) {
		e := newBoard(t, core.StatusActive)
		if e.mayClaimSession(thread, "tok-live") {
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
