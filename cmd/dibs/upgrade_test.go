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
	p := &plan{dir: t.TempDir(), running: daemonState{addr: "192.168.1.205:4777"}}
	joined := strings.Join(checkArgs(p), " ")
	if !strings.Contains(joined, "192.168.1.205:4777") {
		t.Errorf("the proof runs `dibd %s`, which does not name the address the "+
			"replacement will be started on. Everything about binding and TLS is "+
			"then answered for a different daemon, after which the running one is "+
			"stopped", joined)
	}
	if !strings.Contains(joined, "-dir "+p.dir) {
		t.Errorf("the proof does not name the board: %s", joined)
	}

	// AND THE TRANSPORT, when the configuration is describing THIS address.
	//
	// The registry stores a bare host:port, so handing it back makes the
	// replacement re-infer and the two can disagree. Stating it is only safe
	// when the config names the same listener: a daemon started with `-addr`
	// against a config that names nothing resolves loopback, and asserting
	// http:// on a TLS wildcard board is worse than saying nothing, because a
	// bare address still resolves correctly and a wrong one cannot.
	//
	// This test pinned the bare form and could not fail on either case; then it
	// required a scheme unconditionally and could not fail on the wrong-guess
	// case. Both halves now.
	t.Setenv("DIBS_DIR", p.dir)
	if err := os.WriteFile(filepath.Join(p.dir, "dibs.toml"),
		[]byte("addr = \"192.168.1.205:4777\"\ninsecure_plaintext = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := addrFlag(checkArgs(p)); got != "http://192.168.1.205:4777" {
		t.Errorf("the proof is given %q for a board the config describes as plaintext "+
			"on that exact address. The transport is derivable here and stating it "+
			"is the whole point", got)
	}

	// A daemon on an address the config does not name: no guess.
	other := &plan{dir: p.dir, running: daemonState{addr: "10.0.0.9:4777"}}
	if got := addrFlag(checkArgs(other)); got != "10.0.0.9:4777" {
		t.Errorf("the proof is given %q for an address this config says nothing "+
			"about. Inventing a transport there restarts a TLS board in plaintext; "+
			"the bare form lets the replacement resolve it the same way the "+
			"original did", got)
	}

	// The restart must be given the SAME thing, or the proof was about a
	// different daemon than the one that comes up.
	if got, want := replacementAddr(p.dir, p.running.addr), addrFlag(checkArgs(p)); got != want {
		t.Errorf("the restart uses %q and the preflight used %q", got, want)
	}

	// A daemon whose address was never recorded still gets checked, on whatever
	// it resolves for itself: an empty -addr flag would be worse than none.
	if strings.Contains(strings.Join(checkArgs(&plan{dir: p.dir}), " "), "-addr") {
		t.Errorf("an empty address was passed as a flag: %v", checkArgs(&plan{dir: p.dir}))
	}
}

// addrFlag returns the value after -addr, or "".
func addrFlag(args []string) string {
	for i, a := range args {
		if a == "-addr" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// A unit that names a different board is not the way back.
//
// --adopt-dir renames the data directory and rewrites the unit to match. When
// that rewrite fails, recovery runs with the CORRECT new directory in hand and
// used to start the unit anyway, which still pointed at the path that had just
// been moved out from under it: a daemon started against a directory that no
// longer exists, printed as a recovery.
func TestAUnitThatNamesAnotherBoardIsNotUsedForRecovery(t *testing.T) {
	dir := t.TempDir()
	unit := filepath.Join(t.TempDir(), "com.example.dibs.plist")

	if err := os.WriteFile(unit, []byte("<string>-dir</string><string>"+dir+"</string>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !unitNames(unit, dir) {
		t.Error("a unit that names this board was rejected, which would downgrade a " +
			"supervised service to an orphan process that dies at the next logout")
	}

	moved := t.TempDir()
	if unitNames(unit, moved) {
		t.Errorf("a unit naming %s was accepted as the way to start %s. It starts a "+
			"daemon against the directory that was just renamed away, and the "+
			"command reports a recovery", dir, moved)
	}

	// An unreadable unit is trusted, deliberately: refusing over a permissions
	// problem is the worse failure.
	if !unitNames(filepath.Join(t.TempDir(), "absent.plist"), dir) {
		t.Error("an unreadable unit was rejected, so a permissions problem would " +
			"silently turn a supervised daemon into an orphan process")
	}

	// A SUBSTRING IS NOT A NAME. `~/.dibs-old` contains `~/.dibs`, and after an
	// --adopt-dir rename that is the likeliest spelling of the wrong board: the
	// first version of this accepted it, which is the exact case the check
	// exists to catch.
	sub := filepath.Join(t.TempDir(), ".dibs")
	subUnit := filepath.Join(t.TempDir(), "sub.plist")
	if err := os.WriteFile(subUnit, []byte("<string>"+sub+"-old</string>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if unitNames(subUnit, sub) {
		t.Errorf("a unit naming %s-old was accepted as naming %s. That is a different "+
			"board whose path merely starts the same way, and recovery would start "+
			"the daemon against it", sub, sub)
	}

	// AND A PLIST IS XML. A board at a path containing `&` is written `&amp;`,
	// so a raw search rejected the unit as somebody else's and demoted a
	// supervised daemon to a direct start over an ampersand.
	amp := filepath.Join(t.TempDir(), "Fleet & Review")
	ampUnit := filepath.Join(t.TempDir(), "amp.plist")
	if err := os.WriteFile(ampUnit,
		[]byte("<string>"+strings.ReplaceAll(amp, "&", "&amp;")+"</string>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !unitNames(ampUnit, amp) {
		t.Errorf("a unit naming %s, XML-escaped as a plist requires, was not "+
			"recognised as naming it", amp)
	}
}
