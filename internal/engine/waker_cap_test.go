package engine

import "testing"

// A COMMAND THAT HAS NEVER WORKED IS NOT A ROUTE.
//
// The per-message retry is right for a command that failed for a reason the
// next message might not hit. It is the wrong shape for the other case: an
// operator's command that exits non-zero every time, for every agent, forever.
// One board logged "the next message somebody is blocked on will try again"
// about a CLI whose credentials had expired, and meant it, all day, four
// activations in twenty minutes. A retry that repeats identically cannot fix
// the thing it is retrying.
func TestAWakeCommandThatAlwaysFailsIsGivenUpOn(t *testing.T) {
	e := New(nil, &memLedger{}, deadProber{})
	argv := []string{"/usr/bin/false"}

	e.wakers.mu.Lock()
	if e.spawnGivenUp("seat") {
		t.Fatal("setup: given up before anything failed")
	}
	e.wakers.mu.Unlock()

	for i := 1; i < spawnFailureCap; i++ {
		e.noteCommandOutcome("seat", argv, false)
		e.wakers.mu.Lock()
		given := e.spawnGivenUp("seat")
		e.wakers.mu.Unlock()
		if given {
			t.Fatalf("gave up after %d failure(s); the retry that fixes a transient "+
				"fault has to survive", i)
		}
	}
	e.noteCommandOutcome("seat", argv, false)
	e.wakers.mu.Lock()
	given := e.spawnGivenUp("seat")
	e.wakers.mu.Unlock()
	if !given {
		t.Fatalf("still running a command that has failed %d times in a row", spawnFailureCap)
	}

	// A WAKE THAT WORKS CLEARS IT, because the fault was transient after all
	// and an agent that can be reached must not stay written off.
	e.noteCommandOutcome("seat", argv, true)
	e.wakers.mu.Lock()
	given = e.spawnGivenUp("seat")
	e.wakers.mu.Unlock()
	if given {
		t.Error("a successful wake did not clear the count, so one good wake " +
			"leaves the agent unreachable anyway")
	}
}

// And so does changing the configuration, which is what fixing the command
// looks like from the operator's side.
func TestFixingTheConfigurationStartsTryingAgain(t *testing.T) {
	e := New(nil, &memLedger{}, deadProber{})
	for i := 0; i < spawnFailureCap; i++ {
		e.noteCommandOutcome("seat", []string{"/usr/bin/false"}, false)
	}
	e.wakers.mu.Lock()
	given := e.spawnGivenUp("seat")
	e.wakers.mu.Unlock()
	if !given {
		t.Fatal("setup: not given up, so the reload below proves nothing")
	}
	e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{"echo", "hi"}}})
	e.wakers.mu.Lock()
	given = e.spawnGivenUp("seat")
	e.wakers.mu.Unlock()
	if given {
		t.Error("the operator corrected the configuration and the board still " +
			"refuses to run the command")
	}
}
