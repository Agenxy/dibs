package engine

import (
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// A turn that starts after a long idle must suppress the duplicate wake. The
// starting hook deleted the earlier stop but recorded no liveness, so if the
// last authenticated contact predated the cooldown, recentlyInTouch still read
// the agent as idle and a question arriving before the model's first Dibs call
// launched a wake against the thread that had just started. A start is contact.
func TestAStartingHookAfterALongIdleSuppressesTheWake(t *testing.T) {
	e := &Engine{}
	st := core.NewState("t", core.DefaultLimits())
	e.state = st
	e.seen = map[string]time.Time{}
	e.SetWakeCommands(map[string]WakeCommand{
		"codex": {Argv: []string{"echo", "{thread}"}, Cooldown: 90 * time.Second},
	})
	a := bridgeAgent("idle", "Codex", "019ffe52-0eaf-7f60-81cc-6ab1298d76ec")
	st.Agents = map[string]*core.Agent{"idle": a}

	// Its last contact is well OUTSIDE the cooldown: a genuinely idle thread.
	e.seen["idle"] = time.Now().Add(-10 * time.Minute)
	// The harness says a turn ended, then a new one started.
	e.noteTurnState(a, "", "Stop")
	e.noteTurnState(a, "", "SessionStart")
	// A question arrives before the model makes its first Dibs call this turn.
	e.maybeWake(core.Event{
		Type: "message.sent", To: "idle",
		Data: map[string]any{"msg_type": core.MsgQuestion, "from": "asker"},
	})
	if e.wakeSpent("idle") {
		t.Error("a question right after SessionStart launched a wake against a thread that " +
			"just started: deleting the stop was not enough, and the last call predated the " +
			"cooldown, so the running thread read as idle")
	}
}

// A late Stop from a thread the agent has LEFT must not overwrite the liveness
// of the thread it moved to. The row retains every thread it was bound to, so
// the hook resolved to the same row; recording the stop against it read the
// running thread as finished and let the next blocking message launch a second
// activation on it.
func TestALateStopFromAnOldThreadDoesNotMarkTheCurrentOneFinished(t *testing.T) {
	e := &Engine{}
	st := core.NewState("t", core.DefaultLimits())
	e.state = st
	e.seen = map[string]time.Time{}
	e.SetWakeCommands(map[string]WakeCommand{
		"codex": {Argv: []string{"echo", "{thread}"}, Cooldown: 90 * time.Second},
	})
	threadA := "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	threadB := "019ffe52-0eaf-7f60-81cc-6ab1298d76ed"
	a := bridgeAgent("mover", "Codex", threadA)
	// It has moved to thread B: B is current, A is a retained alias.
	a.SessionAliases = []string{threadA, threadB}
	a.CurrentSession = threadB
	st.Agents = map[string]*core.Agent{"mover": a}

	// B checked in just now: it is running.
	e.seen["mover"] = time.Now()
	// A late Stop from the OLD thread A arrives.
	e.noteTurnState(a, threadA, "Stop")
	// Blocking mail for the agent must not now launch a second activation on B.
	e.maybeWake(core.Event{
		Type: "message.sent", To: "mover",
		Data: map[string]any{"msg_type": core.MsgQuestion, "from": "asker"},
	})
	if e.wakeSpent("mover") {
		t.Error("a Stop from the thread the agent LEFT marked the current thread finished, " +
			"and the next question launched a second activation on a thread that is running")
	}

	// A Stop from the CURRENT thread still ends the turn, so the fix does not
	// blind the guard to real stops.
	e.noteTurnState(a, threadB, "Stop")
	e.maybeWake(core.Event{
		Type: "message.sent", To: "mover",
		Data: map[string]any{"msg_type": core.MsgQuestion, "from": "asker2"},
	})
	if !e.wakeSpent("mover") {
		t.Error("a Stop from the CURRENT thread did not end the turn: blocking mail got no wake")
	}
}
