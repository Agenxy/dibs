package main

import (
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
