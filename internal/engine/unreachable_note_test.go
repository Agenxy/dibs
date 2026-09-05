package engine

import (
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A sender is not promised a wake that cannot happen.
//
// `send` to a sleeping recipient returned "it will see this when it next
// wakes". True when something can wake it, and a lie otherwise, in the one
// sentence the sender acts on. Measured: a question to an idle codex agent with
// no thread id, accepted, that promise returned, no wake attempted anywhere in
// the daemon log, unread an hour later.
//
// The fold cannot decide this and must not try. Whether a wake is possible
// depends on the operator's [wake.exec] configuration, which is impure, not
// replayable, and changes without an op. The engine knows; the fold does not;
// so where the engine has something to say it is the half that is true.
//
// The same shape core already fixed one branch over for a superseded sibling,
// where the comment records: "told the senders it would be seen when it next
// wakes. Nobody was coming."
func TestASenderIsNotPromisedAWakeThatCannotHappen(t *testing.T) {
	const thread = "01a0696b-8446-7821-a992-9dc7f6a43a25"

	agent := func(harness, sid string) *core.Agent {
		return &core.Agent{
			ID: "sleeper", Name: "sleeper", Status: core.StatusDormant,
			SessionID: sid, Agent: &core.AgentInfo{Harness: harness, CWD: "/work"},
			Slots: map[string]core.Slot{},
		}
	}
	withWake := func(t *testing.T) *Engine {
		t.Helper()
		e := New(core.NewState("test", core.DefaultLimits()), &memLedger{}, deadProber{})
		e.SetWakeCommands(map[string]WakeCommand{
			"codex": {Argv: []string{"/bin/echo", "{message}"}},
		})
		return e
	}

	t.Run("no wake command for its harness", func(t *testing.T) {
		note := withWake(t).PullOnlyNote(agent("Claude Code", thread))
		if note == "" {
			t.Fatal("silence, so the fold's promise of a wake stands unchallenged")
		}
		if !strings.Contains(note, "nothing is going to wake it") {
			t.Errorf("the note does not contradict the promise it exists to correct: %s", note)
		}
	})

	t.Run("a command, but no thread for it to name", func(t *testing.T) {
		note := withWake(t).PullOnlyNote(agent("Codex", "host-1234"))
		if note == "" {
			t.Fatal("silence for an agent whose wake command has nothing to resume")
		}
		if !strings.Contains(note, "thread id") {
			t.Errorf("the note does not say WHY nothing will run: %s", note)
		}
	})

	// And it stays quiet when the promise is true, or the sender reads two
	// notes about one delivery and learns to skim both.
	t.Run("a reachable agent keeps the fold's wording", func(t *testing.T) {
		if note := withWake(t).PullOnlyNote(agent("Codex", thread)); note != "" {
			t.Errorf("warned about an agent that really can be woken: %s", note)
		}
	})
}
