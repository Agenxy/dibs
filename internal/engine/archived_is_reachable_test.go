package engine

import (
	"context"
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
	// A LOOP-BACKED ENGINE, because this test really starts a process.
	//
	// maybeWake spawns a goroutine that runs the operator's command and then
	// logs the result, and its deferred wakeExited goes through query(), which
	// sends on e.ops. On a bare &Engine{} that channel is nil, so the goroutine
	// parks there forever and the test returns with it still alive. It then logs
	// into whatever global slog handler the NEXT test has installed, and
	// hooklogging_test.go installs a bytes.Buffer of its own: a data race
	// between two tests that never run at the same time.
	//
	// Every local run passed and CI went red, which is how a race behaves and
	// is the second time this file's neighbours have recorded that sentence. A
	// running loop serves the query, so wakeExited completes, wakers.running
	// clears, and awaitWakeDone below has something real to wait on.
	// EVERY SEED BEFORE THE LOOP STARTS. boot() reads the whole of state on the
	// way up, so assigning st.Agents after `go e.Run` is the test racing its own
	// engine. Caught here by -race on the first attempt at this fix, which is
	// the argument for making the fix under -race rather than after it.
	st := core.NewState("t", core.DefaultLimits())
	l := bridgeAgent("swept", "Codex", "019ffe52-0eaf-7f60-81cc-6ab1298d76ec")
	// What the sweep leaves behind: archived, token gone, and the StaleReason
	// that says it went dark rather than finishing.
	l.Status = core.StatusArchived
	l.ArchivedAt = time.Now().Add(-time.Hour)
	l.StaleReason = "idle_no_activity"
	l.Token = ""
	l.LastCoordination = time.Now().Add(-2 * time.Hour)
	st.Agents = map[string]*core.Agent{"swept": l}

	e := New(st, &memLedger{}, deadProber{})
	e.SetWakeCommands(map[string]WakeCommand{
		"codex": {Argv: []string{"echo", "{thread}"}, Cooldown: time.Minute},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	e.maybeWake(core.Event{
		Type: "message.sent", To: "swept",
		Data: map[string]any{"msg_type": core.MsgQuestion},
	})
	if !e.wakeSpent("swept") {
		t.Error("no wake for an agent the sweep archived: its mail and its nonce " +
			"are both still there, so it can come back, and nothing will ever " +
			"tell it to")
	}
	awaitWakeDone(t, e, "swept")
}

// awaitWakeDone waits for the goroutine maybeWake started to finish.
//
// Not tidiness: that goroutine logs, and a test which returns while it is still
// running hands a live writer to whatever global slog handler the next test
// installs. wakers.running is cleared by wakeExited, which runs AFTER the
// command and after its log line, so this is the one signal that covers both.
//
// Requires an engine with a running loop; see the note in the caller.
func awaitWakeDone(t *testing.T, e *Engine, agent string) {
	t.Helper()
	// SEEN RUNNING FIRST, because "not running" is equally true of a wake that
	// never started, and a waiter that returns on its first poll waits for
	// nothing while reading exactly like a waiter that works. wakeFor sets this
	// SYNCHRONOUSLY, inside maybeWake and before the goroutine, so by the time
	// the caller reaches here it is true or there was no wake at all: no race,
	// no sleep, and a real assertion rather than a hopeful one.
	//
	// The first version of this checked wakers.attempts instead and hung, which
	// is how the decoration was caught: a successful wake CLEARS its attempt
	// count, so the condition could never hold. Five clean -race runs had
	// already been collected with the version before that, which returned
	// immediately and proved nothing.
	e.wakers.mu.Lock()
	started := e.wakers.running[agent]
	e.wakers.mu.Unlock()
	if !started {
		t.Fatalf("no wake is running for %s, so there is nothing to wait for and "+
			"this helper is measuring nothing", agent)
	}
	// A BOUNDED WAIT, not a sleep, because the exit has no channel to offer: it
	// is a map entry cleared on the writer loop by wakeExited, which runs after
	// the command AND after its log line, so it covers both. The deadline is the
	// assertion; the tick is only how often the question is asked. Same shape as
	// the socket-wake waits elsewhere in this package.
	deadline := time.After(10 * time.Second)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		e.wakers.mu.Lock()
		running := e.wakers.running[agent]
		e.wakers.mu.Unlock()
		if !running {
			return
		}
		select {
		case <-tick.C:
		case <-deadline:
			t.Fatalf("the wake started for %s never finished, so this test would "+
				"leak a goroutine that logs into the next test's handler", agent)
		}
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
