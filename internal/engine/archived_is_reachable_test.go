package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// ARCHIVED BY A TIMER IS NOT THE SAME AS SIGNED OFF, and the wake path treated
// them as one thing.
//
// maybeWake asked Gone(), which is Closed || Archived, and the comment above it
// argues only the Closed half: waking a signed-off persistent identity resumes
// it through the nonce path and brings it back ACTIVE, defeating the finality
// sign_off promises. That is right, and none of it is true of Archived, which
// nobody chose. Archived is what the sweep does to an agent that stopped
// talking, and on the shipped defaults it does it to an EPHEMERAL agent after
// AgentTTL + StaleGrace: five minutes plus thirty.
//
// So an ordinary agent doing ordinary work between two coordination calls
// became unwakeable thirty-five minutes later, while its mail sat in the
// ledger and its nonce index entry sat in state for another seven days,
// recoverable the whole time (core.TestAnArchivedAgentComesBackWithItsNonce...
// pins that recovery). Recoverable and unreachable: the one combination that
// makes the recovery useless, because nothing tells the agent to come back.
//
// The board's whole promise is reaching an agent that is not running. A timer
// that silently withdraws it is the promise expiring, not the agent.
func TestAnAgentArchivedByRetentionIsStillWoken(t *testing.T) {
	e := &Engine{}
	st := core.NewState("t", core.DefaultLimits())
	e.state = st
	e.SetWakeCommands(map[string]WakeCommand{
		"codex": {Argv: []string{"echo", "{thread}"}, Cooldown: time.Minute},
	})
	l := bridgeAgent("swept", "Codex", "019ffe52-0eaf-7f60-81cc-6ab1298d76ec")
	// What the sweep leaves behind: archived, token gone, and the StaleReason
	// that says it went dark rather than finishing.
	l.Status = core.StatusArchived
	l.ArchivedAt = time.Now().Add(-time.Hour)
	l.StaleReason = "idle_no_activity"
	l.Token = ""
	l.LastCoordination = time.Now().Add(-2 * time.Hour)
	st.Agents = map[string]*core.Agent{"swept": l}

	e.maybeWake(core.Event{
		Type: "message.sent", To: "swept",
		Data: map[string]any{"msg_type": core.MsgQuestion},
	})
	if !e.wakeSpent("swept") {
		t.Error("no wake for an agent the sweep archived: its mail and its nonce " +
			"are both still there, so it can come back, and nothing will ever " +
			"tell it to")
	}
}

// The other half, which must not move. sign_off is final and says so.
func TestAnAgentThatSignedOffIsStillNotWoken(t *testing.T) {
	e := &Engine{}
	st := core.NewState("t", core.DefaultLimits())
	e.state = st
	e.SetWakeCommands(map[string]WakeCommand{
		"codex": {Argv: []string{"echo", "{thread}"}, Cooldown: time.Minute},
	})
	l := bridgeAgent("finished", "Codex", "019ffe52-0eaf-7f60-81cc-6ab1298d76ec")
	l.Status = core.StatusClosed
	l.LastCoordination = time.Now().Add(-2 * time.Hour)
	st.Agents = map[string]*core.Agent{"finished": l}

	e.maybeWake(core.Event{
		Type: "message.sent", To: "finished",
		Data: map[string]any{"msg_type": core.MsgQuestion},
	})
	if e.wakeSpent("finished") {
		t.Error("started a process for an agent that deliberately signed off: " +
			"resuming it would bring the identity back active, which is exactly " +
			"the finality sign_off promises")
	}
}

// And the sender of mail to an archived agent nothing can wake is told so.
//
// PullOnlyNote returned "" on Gone(), so the one recipient state that most
// needs the warning produced none: an archived agent's harness session has
// ended, so the socket route is not a rescue, and without a [wake.exec] entry
// and a thread id there is no route at all. The sender saw a bare ok and a
// deadline it had no way to know was hopeless.
func TestTheSenderIsWarnedAboutAnArchivedAgentNothingCanWake(t *testing.T) {
	e := &Engine{}
	st := core.NewState("t", core.DefaultLimits())
	e.state = st
	// No wake commands configured at all: nothing on this board can start it.
	l := bridgeAgent("swept", "Codex", "019ffe52-0eaf-7f60-81cc-6ab1298d76ec")
	l.Status = core.StatusArchived
	l.ArchivedAt = time.Now().Add(-time.Hour)
	l.StaleReason = "idle_no_activity"
	st.Agents = map[string]*core.Agent{"swept": l}

	note := e.PullOnlyNote(l)
	if note == "" {
		t.Fatal("no note for an archived recipient nothing can wake: the sender is " +
			"left believing the message will be seen when the agent next wakes, and " +
			"nothing is going to wake it")
	}
	if !strings.Contains(note, string(core.StatusArchived)) {
		t.Errorf("the note does not say what state the recipient is in:\n  %s", note)
	}
	if !strings.Contains(note, "Nothing will start it") {
		t.Errorf("the note does not say plainly that nothing will start it, which is "+
			"the whole point of saying anything:\n  %s", note)
	}
}

// An archived agent still holding blocking mail is retried at boot.
//
// The deferred wake is a timer and a restart loses it, which is what
// rearmDeferredWakes exists for. It asked Gone() too, so the agents most likely
// to be sitting on stranded mail (the ones swept while nobody was looking)
// were the ones it skipped.
func TestBootRearmsTheWakeForArchivedAgentsHoldingMail(t *testing.T) {
	e := &Engine{}
	st := core.NewState("t", core.DefaultLimits())
	e.state = st
	l := bridgeAgent("swept", "Codex", "019ffe52-0eaf-7f60-81cc-6ab1298d76ec")
	l.Status = core.StatusArchived
	l.ArchivedAt = time.Now().Add(-time.Hour)
	l.StaleReason = "idle_no_activity"
	st.Agents = map[string]*core.Agent{"swept": l}
	st.Messages = map[uint64]*core.Message{
		1: {Serial: 1, To: "swept", From: "peer", Type: core.MsgQuestion, State: core.MsgStatePending},
	}

	if n := e.rearmDeferredWakes(); n != 1 {
		t.Errorf("rearmDeferredWakes armed %d retries, want 1: the archived agent "+
			"is holding a question nobody will ever wake it for", n)
	}
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	for a := range e.wakers.deferred {
		e.wakers.deferred[a].Stop()
	}
}
