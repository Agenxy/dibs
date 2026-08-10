package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/agenxy/lanes/internal/paths"
)

// This function decides which process gets a signal. Getting it wrong kills
// somebody else's fleet and reports success, which is the failure `pkill
// lanesd` had and the reason `lanes stop` exists.
func TestStopSignalsOnlyTheDaemonForThisDirectory(t *testing.T) {
	const mine = "/tmp/mine"

	t.Run("picks ours and counts the rest", func(t *testing.T) {
		d, others, err := selectDaemon([]paths.Daemon{
			{PID: 11, Dir: "/tmp/someone-else", Addr: "127.0.0.1:4001"},
			{PID: 22, Dir: mine, Addr: "127.0.0.1:4777"},
			{PID: 33, Dir: "/tmp/a-third", Addr: "127.0.0.1:4002"},
		}, mine)
		if err != nil {
			t.Fatal(err)
		}
		if d == nil || d.PID != 22 {
			t.Fatalf("selected %v, want the daemon on %s (pid 22)", d, mine)
		}
		if others != 2 {
			t.Errorf("others = %d, want 2 — the caller reports what it left alone", others)
		}
	})

	t.Run("nothing running is not an error", func(t *testing.T) {
		d, others, err := selectDaemon([]paths.Daemon{
			{PID: 11, Dir: "/tmp/someone-else"},
		}, mine)
		if err != nil {
			t.Fatalf("stopping what is not running should succeed: %v", err)
		}
		if d != nil {
			t.Errorf("selected %v from a registry that contains no daemon for %s", d, mine)
		}
		if others != 1 {
			t.Errorf("others = %d, want 1", others)
		}
	})

	t.Run("an undecodable entry is never ours", func(t *testing.T) {
		// A zero-valued Daemon has an empty Dir, which canonicalises to the
		// working directory and could therefore match. Unknown must lose.
		d, others, err := selectDaemon([]paths.Daemon{
			{PID: 44, Unknown: true},
		}, mine)
		if err != nil {
			t.Fatal(err)
		}
		if d != nil {
			t.Fatalf("selected an entry we could not decode (pid %d) — signalling a pid "+
				"we cannot attribute is the exact mistake this guards", d.PID)
		}
		if others != 1 {
			t.Errorf("others = %d, want 1", others)
		}
	})

	t.Run("two claimants refuses rather than guessing", func(t *testing.T) {
		_, _, err := selectDaemon([]paths.Daemon{
			{PID: 55, Dir: mine}, {PID: 66, Dir: mine},
		}, mine)
		if err == nil {
			t.Fatal("two daemons claiming one directory should refuse, not pick one")
		}
	})
}

// `lanes stop --help` stopped the daemon and exited 0: asking a destructive
// command what it does performed it. Second time in this CLI — configure.go
// carries a comment about `lanes configure --help` being read as a directory
// named "--help" — so this asserts the shape, not just the one flag.
func TestStopInspectsItsArgumentsBeforeSignallingAnything(t *testing.T) {
	for _, arg := range []string{"-h", "--help", "help"} {
		if err := stop([]string{arg}); err != nil {
			t.Errorf("stop(%q) returned %v; it should print help and do nothing", arg, err)
		}
	}
	// Anything else is refused rather than ignored. Ignoring an unrecognised
	// argument is how `--dry-run` becomes a live run.
	for _, arg := range []string{"--dry-run", "--force", "/some/other/dir", "-n"} {
		if err := stop([]string{arg}); err == nil {
			t.Errorf("stop(%q) was accepted — an unrecognised argument to a destructive "+
				"command must refuse, not proceed", arg)
		}
	}
}

// The bug was in the DISPATCH, not in stop(): main called stopDaemon directly
// and threw os.Args[2:] away, so `lanes stop --help` signalled the daemon. A
// test that calls stop() cannot see that — reverting the dispatch leaves it
// green, which is how a correct-but-unwired guard ships.
//
// This is the same shape as internal/engine/admit_wired_test.go and
// internal/mcp/schema_reach_test.go, and it is the codebase's most repeated
// defect.
func TestTheStopVerbIsWiredThroughArgumentChecking(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	const dispatch = `case "stop":`
	i := bytes.Index(src, []byte(dispatch))
	if i < 0 {
		t.Fatalf("no %q in main.go — the verb was renamed; update this test", dispatch)
	}
	// The next non-blank line is what the verb actually runs.
	body := string(src[i : i+220])
	if !strings.Contains(body, "stop(os.Args[2:])") {
		t.Errorf("`lanes stop` does not route through the argument check.\n"+
			"  Calling stopDaemon directly means flags are ignored, and `lanes stop --help`\n"+
			"  performs the stop. Dispatch found:\n%s", body)
	}
}
