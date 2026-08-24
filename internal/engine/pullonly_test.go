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

	// And once a wake command IS configured for that harness, it goes quiet.
	// Without this half the function could simply always warn, which would be
	// the habituation failure this release spent three commits on.
	e.wakers.mu.Lock()
	if e.wakers.byHarness == nil {
		e.wakers.byHarness = map[string]wakeCommand{}
	}
	e.wakers.byHarness["some-harness"] = wakeCommand{argv: []string{"echo", "wake"}}
	e.wakers.mu.Unlock()

	if n := e.PullOnlyNote(l); n != "" {
		t.Errorf("still warning about an agent this board CAN wake: %q", n)
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
