package engine

import (
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// Sending to an ACTIVE agent nobody can wake must say so.
//
// `send` warns when the recipient is dormant. It said nothing when the
// recipient was active on a harness with no wake path, and that is the more
// misleading of the two: an active row plus a silent ok reads as "this will
// arrive shortly", when it actually arrives whenever a human next types into
// that session. Measured on a live board, where a request carrying a
// ninety-minute deadline reached an agent that had coordinated four minutes
// earlier, and nothing stirred for eight.
//
// Nothing is broken when this fires. Some harnesses are pull-only by design and
// Dibs will not spawn a process to drive one (PHILOSOPHY rule 5). The defect
// was the silence.
func TestSendingToAnUnwakeableActiveAgentSaysSo(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})

	if _, _, err := st.Apply(&core.Op{
		Kind: core.OpRegister, Name: "puller", NewToken: "tok",
		Agent: &core.AgentInfo{Harness: "some-harness"},
	}, t0Engine()); err != nil {
		t.Fatal("setup:", err)
	}
	l := st.Agents["puller"]
	// Setup must hold, or the assertion below is about the wrong branch.
	if l.Sleeping() || l.Gone() {
		t.Fatalf("setup: the recipient is %s, not active, so core would warn instead",
			l.Status)
	}

	note := e.PullOnlyNote(l)
	if note == "" {
		t.Fatal("no warning for an active agent on a harness with no wake command. " +
			"An active row and a silent ok reads as 'this will arrive shortly'")
	}
	if !strings.Contains(note, "pull-only") || !strings.Contains(note, "some-harness") {
		t.Errorf("the warning does not name the harness or what is wrong with it: %q", note)
	}

	// CONFIGURING A COMMAND IS NOT ENOUGH, and my first version of this test
	// stopped here and asserted it was.
	//
	// wakeFor needs a UUID-shaped thread id as well: without one it returns
	// before starting anything. This fixture has no thread id, so adding an
	// argv makes the board configured and still incapable, and the warning must
	// STAY. The earlier test asserted the opposite and called this recipient one
	// the board "CAN wake", which pinned the silent-success bug it was written
	// to prevent. Found by the pre-release review.
	e.wakers.mu.Lock()
	if e.wakers.byHarness == nil {
		e.wakers.byHarness = map[string]wakeCommand{}
	}
	e.wakers.byHarness["some-harness"] = wakeCommand{argv: []string{"echo", "wake"}}
	e.wakers.mu.Unlock()

	if threadIDOf(l) != "" {
		t.Fatal("setup: this fixture is supposed to have no thread id, which is the " +
			"whole point of the case below")
	}
	configured := e.PullOnlyNote(l)
	if configured == "" {
		t.Error("went quiet as soon as a wake command was configured, while the agent " +
			"still has no thread id for it to resume. wakeFor will not run the command, " +
			"so the sender gets neither a wake nor a warning: the exact silent success " +
			"this note exists to remove")
	}
	if !strings.Contains(configured, "thread id") {
		t.Errorf("the warning does not say WHY a configured harness still cannot be "+
			"woken, which is the one thing an operator could act on: %q", configured)
	}

	// And with both halves present, configured AND resumable, it finally goes
	// quiet. Without this the function could just always warn.
	l.SessionAliases = append(l.SessionAliases, "3f2504e0-4f89-11d3-9a0c-0305e82c3301")
	if threadIDOf(l) == "" {
		t.Fatal("setup: the alias was not accepted as a thread id, so the case below " +
			"is not the one intended")
	}
	if n := e.PullOnlyNote(l); n != "" {
		t.Errorf("still warning about an agent this board genuinely CAN wake, with a "+
			"command configured and a thread to resume: %q", n)
	}
}

// A sleeping recipient keeps core's better sentence, and does not get both.
func TestASleepingRecipientIsNotWarnedTwice(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	if _, _, err := st.Apply(&core.Op{
		Kind: core.OpRegister, Name: "sleeper", NewToken: "tok",
		AgentKind: core.KindPersistent, Nonce: "n-sleeper",
		Agent: &core.AgentInfo{Harness: "some-harness"},
	}, t0Engine()); err != nil {
		t.Fatal("setup:", err)
	}
	l := st.Agents["sleeper"]
	l.Status = core.StatusDormant
	if !l.Sleeping() {
		t.Fatal("setup: the recipient is not sleeping, so this tests nothing")
	}
	if n := e.PullOnlyNote(l); n != "" {
		t.Errorf("a dormant recipient got the pull-only note as well as core's own "+
			"sleeping note: two warnings about one delivery: %q", n)
	}
}
