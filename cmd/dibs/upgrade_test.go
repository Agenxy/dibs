package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// "too old to be asked" and "asked, and the answer was no" are opposite facts
// that arrive identically: a non-zero exit with output on stderr. Only one of
// them means the board is in trouble, and telling an operator the wrong one
// during an upgrade is how a healthy fleet gets treated as a broken one. The
// first real run of `dibs upgrade` did exactly that.
func TestAnOldDaemonIsNotReportedAsAnUnrebuildableBoard(t *testing.T) {
	old := []byte("flag provided but not defined: -check\nUsage of /usr/local/bin/dibd:\n  -addr string")
	if !tooOldForCheck(old) {
		t.Error("a daemon predating -check was read as a failed check")
	}
	// The shape the check itself fails with: it names the board, not the flag.
	failed := []byte("this build cannot rebuild the board at /Users/x/.dibs: " +
		"replay apply serial 416: E_AGENT_CLOSED")
	if tooOldForCheck(failed) {
		t.Error("a genuine replay failure was excused as an old binary, which would " +
			"let the upgrade proceed onto a daemon that cannot serve the board")
	}
}

// An unknown argument is a misunderstanding about a command that restarts a
// daemon a fleet is talking to. `dibs stop --help` once performed the stop;
// this one refuses rather than guessing, and refuses before anything runs.
func TestUpgradeRefusesArgumentsItDoesNotUnderstand(t *testing.T) {
	err := upgradeCmd([]string{"--force"})
	if err == nil {
		t.Fatal("an unrecognised flag was accepted by a command that restarts the daemon")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("err = %v: it does not name the argument it refused", err)
	}
	// And help is help, whatever else was typed alongside it.
	if err := upgradeCmd([]string{"--help"}); err != nil {
		t.Errorf("--help returned %v", err)
	}
}

// The adopted name sits BESIDE the inherited directory, never inside it.
// Renaming ~/.agents to ~/.agents/.dibs would move a directory into itself.
func TestTheAdoptedDirectoryIsASiblingNotAChild(t *testing.T) {
	got := adoptedName("/Users/x/.agents")
	if want := "/Users/x/.dibs"; got != want {
		t.Errorf("adoptedName = %q, want %q", got, want)
	}
	if strings.HasPrefix(got, "/Users/x/.agents"+string(filepath.Separator)) {
		t.Error("the target is inside the directory being moved")
	}
}

// A service manager reporting success is not a daemon that is serving.
//
// `launchctl kickstart` exits 0 having merely SCHEDULED a spawn, and launchd
// reads a plist at LOAD time: rewriting the file changes nothing it knows, so
// it schedules the OLD program, which may not exist. Measured on a live board,
// where the plist on disk named the new daemon while `launchctl print` showed
// `program = ~/go/bin/dibd` and `active count = 0`, and the board stayed down.
//
// Two rules came out of that, and this pins the one a refactor would quietly
// undo: the daemon is not considered restored until the BOARD answers, so the
// recovery still fires when the start reported success and produced nothing.
//
// WHAT THIS DOES NOT PROVE, said plainly because a reader who skims it will
// otherwise assume more. It reads the SOURCE for an ordering. It never runs a
// failed cutover, never starts a daemon, and never shows the recovery putting a
// board back. A pre-release review made exactly that criticism and it is
// correct: two defects lived inside the recovery this test sits next to, a
// directory that had been renamed out from under it and a message claiming a
// rollback that never happens, and neither is the kind of thing an ordering
// check can see. Proving those needs cutover's start, stop and verify to be
// injectable, which is a refactor rather than a test.
//
// It is kept because the ordering is genuinely load-bearing and a structural
// check catches a genuine regression class. It is not a behavioural guarantee
// and must not be quoted as one.
func TestARestartIsNotBelievedUntilTheBoardAnswers(t *testing.T) {
	src, err := os.ReadFile("upgrade.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	verify := strings.Index(body, "if err := p.verify(newDir); err != nil {")
	restored := strings.Index(body, "\trestored = true\n")
	if verify < 0 || restored < 0 {
		t.Fatal("cutover no longer verifies the board or no longer marks the restart " +
			"restored: re-read this test's reasoning before changing it")
	}
	if restored < verify {
		t.Error("`restored = true` runs before the board is verified, so a start that " +
			"reported success and served nothing would leave the fleet with no daemon " +
			"and the recovery disabled: exactly the failure measured on a live board")
	}
	// And the unit is reloaded unconditionally, not only when this command
	// rewrote it: a plist edited by hand, or by an earlier run that then
	// failed, drifts the same way and presents identically.
	if !strings.Contains(body, "func restartUnit(unit string) error {") ||
		!strings.Contains(body, "if err := reloadUnit(unit); err != nil {") {
		t.Error("restartUnit no longer reloads the unit before restarting it")
	}
}

// The proof must be about the daemon that will actually be started.
//
// `proveReplacement` runs `dibd -check` and treats a zero exit as licence to
// stop the running daemon. It passed only `-dir`, while the replacement is
// started a few steps later with `-addr <what the running daemon bound>`: so
// everything address- and TLS-specific was proved for the DEFAULT address and
// then not used. Recovery retries this same binary rather than the previous
// build, so a failure at that point leaves the fleet down.
//
// This asserts the argv, because the fault was in the argv: the two commands
// have to describe one daemon.
func TestTheReplacementIsProvedForTheAddressItWillBeStartedOn(t *testing.T) {
	p := &plan{dir: "/tmp/board", running: daemonState{addr: "192.168.1.205:4777"}}
	got := checkArgs(p)
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "-addr 192.168.1.205:4777") {
		t.Errorf("the proof runs `dibd %s`, which does not name the address the "+
			"replacement will be started on. Everything about binding and TLS is "+
			"then answered for a different daemon, after which the running one is "+
			"stopped", joined)
	}
	if !strings.Contains(joined, "-dir /tmp/board") {
		t.Errorf("the proof does not name the board: %s", joined)
	}

	// A daemon whose address was never recorded still gets checked, on whatever
	// it resolves for itself: passing an empty -addr would be worse than none.
	bare := checkArgs(&plan{dir: "/tmp/board"})
	if strings.Contains(strings.Join(bare, " "), "-addr") {
		t.Errorf("an empty address was passed as a flag: %v", bare)
	}
}
