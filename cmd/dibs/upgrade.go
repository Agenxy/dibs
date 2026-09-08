package main

import (
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/paths"
	"github.com/agenxy/dibs/internal/ui"
)

// Upgrading is a product feature here, not an operational chore (REQUIREMENTS.md
// R12). Dibs coordinates agents that run for days, so an operator who watches
// one upgrade break a running fleet will stay on an old build forever, and a
// coordination service nobody dares update is worse than one that is briefly
// unavailable.
//
// R12 settled the CLIENT half: the bridge waits out the restart window and
// re-sends only requests that provably never arrived. This is the OPERATOR
// half, and it exists because every failure below was found on a real machine
// and every one of them was, until now, homework:
//
//   - The service unit pins an absolute path to a daemon installed somewhere
//     else, so it starts a build from months ago, forever, and every check
//     passes against the current one somebody started by hand.
//   - The data directory carries a name an older version chose, and the unit
//     pins that path, so adopting the new name breaks the service.
//   - The new binary cannot fold the ledger the old one wrote. This is the one
//     that ends a fleet: by the time it is discovered, the daemon that COULD
//     serve the board has been stopped.
//
// `dibs doctor` finds all three and hands each back as a shell recipe. That is
// the right shape for a diagnosis and the wrong shape for an upgrade: the
// recipes have an order, two of them have to happen while the daemon is down,
// and getting the order wrong is how a board ends up served by a binary that
// cannot rebuild it.
//
// The ordering rule this whole command is built around: **nothing is stopped
// until the replacement has proven it can take over.**
type upgradeOpts struct {
	dryRun   bool
	adoptDir bool
}

func upgradeCmd(args []string) error {
	var o upgradeOpts
	for _, a := range args {
		switch a {
		case "--dry-run", "-n":
			o.dryRun = true
		case "--adopt-dir":
			o.adoptDir = true
		case "--help", "-h":
			fmt.Print(upgradeHelp)
			return nil
		default:
			// Same rule as `dibs stop`: an argument this command does not
			// understand is a misunderstanding about what it does, and it
			// restarts a daemon a fleet is talking to.
			return fmt.Errorf("dibs upgrade: unknown argument %q\n\n%s", a, upgradeHelp)
		}
	}
	return upgrade(o)
}

const upgradeHelp = `dibs upgrade: move the running daemon onto the dibd you have installed.

  It does NOT fetch anything. Install the new build first (brew upgrade, or
  task install from a checkout); this is the step that puts the fleet onto it.
  Run bare on an up-to-date install and it correctly does nothing, which reads
  as a failure if you expected it to go and get the release.

  Checks that the new binary can rebuild this board BEFORE stopping anything,
  reconciles a service unit that pins the wrong daemon, restarts, and verifies
  the fleet came back with its serial and its agents intact.

  --dry-run, -n   say what it would do and change nothing
  --adopt-dir     also move a data directory named by an older version, and
                  repoint the service unit at the new path (daemon stopped for
                  the move; refused if anything about it is ambiguous)
`

// plan is everything upgrade() resolved before it touched anything, so the
// phases below can be read (and tested) as the separate decisions they are.
type plan struct {
	opts               upgradeOpts
	dir, inherited     string
	installed          string
	unit, pinned       string
	unitWrong, moveDir bool
	// checked is the version the installed daemon reported for itself when
	// the preflight asked it to rebuild the board: the replacement's own
	// build, which is not this CLI's. See nothingToDo.
	checked string
	running daemonState
	before  fleet
	serving bool

	// THE THREE EFFECTS CUTOVER HAS ON A LIVE FLEET, behind fields so a test
	// can drive them.
	//
	// The guarantee here is an ORDERING: the restart is not believed until the
	// board answers, so the recovery still fires when a start reported success
	// and produced nothing. That was pinned by a test that read this file for
	// the positions of two string literals, which cannot tell a live path from
	// an unreachable branch and cannot see a recovery that starts the wrong
	// directory: two such defects lived inside this function while that test
	// was green, and a review round said so in those words.
	//
	// Nil means the real thing. Set in planUpgrade, so a plan built anywhere
	// else behaves identically and nothing has to remember to wire them.
	stop    func(dir string) error
	start   func(installed, dir, unit string, was daemonState) error
	confirm func(newDir string) error
	// retryPause is the wait after a start that failed at once; see recover.
	retryPause time.Duration
}

// recoveryStarts bounds how many times a recovery starts the daemon while
// waiting for the board to answer; each wait is the confirm's own limit.
const recoveryStarts = 3

// startExitWatch is how long startDaemon watches a fresh process for an
// immediate exit before calling the start done.
const startExitWatch = 750 * time.Millisecond

// recover starts the daemon again after a cutover that stopped it and could
// not finish, and stays until the board answers or the attempts run out.
func (p *plan) recover(dir string) {
	// A START THAT FAILS AT ONCE IS AN ATTEMPT. After a stop that timed out
	// the old daemon may still hold the directory lock, the replacement
	// exits on it, and startDaemon reports that as the error it is. The
	// first version of this returned on that error before its own retry
	// loop, so the case the loop exists for never reached it. Found by the
	// pre-release review, round twenty-four.
	for attempt := 1; ; attempt++ {
		if err := p.doStart(dir); err != nil {
			if attempt >= recoveryStarts {
				fmt.Fprintf(os.Stderr, "\n%s could not start the daemon in %d attempts: start it "+
					"with `%s -dir %s` once the old process has exited: %v\n",
					ui.Bold("AND:"), attempt, p.installed, dir, err)
				return
			}
			fmt.Fprintf(os.Stderr, "the daemon did not start (%v); starting again (%d of %d)\n",
				err, attempt+1, recoveryStarts)
			p.pause()
			continue
		}
		if err := p.doConfirm(dir); err == nil {
			fmt.Fprintf(os.Stderr, "\n%s the daemon is serving again from %s on %s. "+
				"This is the NEW build, not a rollback: the previous binary is not "+
				"retained. If the failure above was the new build itself, install "+
				"the previous one before relying on it.\n",
				ui.Bold("recovered:"), dir, filepath.Base(p.installed))
			return
		}
		if attempt >= recoveryStarts {
			fmt.Fprintf(os.Stderr, "\n%s the daemon was started %d times from %s and the "+
				"board did not answer. Run `dibs doctor`, or start it with `%s -dir %s` "+
				"once the old process has exited.\n",
				ui.Bold("AND:"), attempt, dir, p.installed, dir)
			return
		}
		fmt.Fprintf(os.Stderr, "the board did not answer; starting again (%d of %d)\n",
			attempt+1, recoveryStarts)
	}
}

// pause is the wait between a start that failed at once and the next: the
// old process is still going, and the confirm's own wait paces the other
// path. A test sets none.
func (p *plan) pause() {
	if p.retryPause > 0 {
		<-time.After(p.retryPause)
	}
}

func (p *plan) doStop(dir string) error {
	if p.stop != nil {
		return p.stop(dir)
	}
	return stopDaemon(dir)
}

func (p *plan) doStart(dir string) error {
	if p.start != nil {
		return p.start(p.installed, dir, p.unit, p.running)
	}
	return startDaemon(p.installed, dir, p.unit, p.running)
}

func (p *plan) doConfirm(newDir string) error {
	if p.confirm != nil {
		return p.confirm(newDir)
	}
	return p.verify(newDir)
}

func say(format string, a ...any) { fmt.Printf(format+"\n", a...) }
func step(s string)               { fmt.Println(ui.Bold("→ ") + s) }

func upgrade(o upgradeOpts) error {
	p, err := planUpgrade(o)
	if err != nil {
		return err
	}
	if o.dryRun {
		return p.report()
	}
	if err := p.preflight(); err != nil {
		return err
	}
	// NOTHING TO DO IS NOTHING DONE. The help said a bare run on an
	// up-to-date install correctly does nothing, and the command then
	// stopped a serving daemon and restarted it onto the build it was
	// already on: a fleet restart for no change. When the daemon reports the
	// build the installed daemon reports for itself, and nothing about the
	// unit needs repair, it says so and stops. Found by the pre-release
	// review, round fifty; compared against the CLI's own version in its
	// first cut, which is not the replacement's, round fifty-one.
	// THE DAEMON THIS COMMAND IS ABOUT TO REPLACE, not the one DIBS_ADDR
	// names: with the registry's target on an older build and another board
	// configured on the new one, asking the configured origin concluded
	// nothing to do and left the target unchanged. A query that fails
	// proceeds to the cutover, which is the safe direction. Found by the
	// pre-release review, round fifty-three.
	if info, ierr := daemonBuildAt(runningOrigin(p)); ierr == nil && p.nothingToDo(info) {
		fmt.Printf("already on %s: the daemon is serving the build you installed, nothing to do\n", info.Version)
		return nil
	}
	return p.cutover()
}

// runningOrigin is where the daemon the plan will replace answers, from the
// registry's record of it: the address it bound, with the scheme it was
// asked for when it recorded one, and plaintext otherwise.
func runningOrigin(p *plan) string {
	a := replacementAddr(p.dir, p.running.addr)
	if _, _, found := strings.Cut(a, "://"); found {
		return a
	}
	return "http://" + a
}

// nothingToDo reports whether the cutover would change nothing: the daemon
// serves the build the installed binary reported for itself, and the
// service unit and data directory need no repair.
func (p *plan) nothingToDo(info buildInfo) bool {
	return p.serving && !p.unitWrong && !p.moveDir && alreadyOn(info, p.checked)
}

// alreadyOn reports whether the serving daemon is on the build the installed
// daemon reports for itself. A development build reports no version worth
// comparing, and two of those are not known to be the same code.
func alreadyOn(info buildInfo, installed string) bool {
	if installed == "" || info.Version == "" || !releasedBuild(installed) || !releasedBuild(info.Version) {
		return false
	}
	return info.Version == installed
}

// releasedBuild reports whether a version string names one build. A local
// build reports `devel`, or `devel+<revision>.dirty`, and two dirty builds
// of one revision are different binaries with the same string: comparing
// them told an operator who had just rebuilt that there was nothing to do
// while the old daemon went on serving. Found by the pre-release review,
// round fifty-five.
func releasedBuild(v string) bool {
	return v != "" && !strings.HasPrefix(v, "devel") && !strings.Contains(v, ".dirty")
}

// checkedVersion reads the version out of a `dibd -check` report, which
// begins `ok: <version> replays ...`, or "" when the line is not that.
func checkedVersion(out []byte) string {
	for _, line := range strings.Split(string(out), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "ok: ")
		if !ok {
			continue
		}
		if v, _, found := strings.Cut(rest, " replays "); found {
			return v
		}
	}
	return ""
}

// planUpgrade resolves what is out of line, and proves the replacement can
// rebuild the board. Nothing here changes anything.
func planUpgrade(o upgradeOpts) (*plan, error) {
	p := &plan{opts: o, retryPause: 10 * time.Second}
	p.dir, p.inherited = paths.Resolve()

	var err error
	if p.installed, err = daemonPath(); err != nil {
		return nil, fmt.Errorf("cannot find an installed dibd: %w", err)
	}
	say("%s %s", ui.Dim("data dir "), ui.Path(p.dir))
	say("%s %s", ui.Dim("installed"), ui.Path(p.installed))

	// How the daemon is CURRENTLY running, captured before it is stopped.
	//
	// Not cosmetic. A daemon serving a fleet across machines is bound to a LAN
	// or tailnet address, and restarting it on the default loopback would take
	// every remote agent off the board while every local check still passed:
	// the exact shape of failure this command exists to prevent, introduced by
	// the command itself. The registry records what each live daemon bound, so
	// the answer is observed rather than assumed.
	p.running = runningDaemon(p.dir)

	// What is serving right now, so the end of this can prove the board came
	// back rather than assert it.
	var serveErr error
	p.before, serveErr = fleetSnapshotAt(p.running.addr)
	p.serving = serveErr == nil
	switch {
	case p.serving:
		say("%s serial %d, %d agent(s)", ui.Dim("running  "), p.before.Serial, p.before.Agents)
	case p.running.addr != "":
		// The registry says one is there and it did not answer. Saying only
		// "nothing is answering" invites the reader to believe the board is
		// already down, which is the assumption that made the stop skippable.
		say("%s registered on %s and not answering (%v): it will still be stopped",
			ui.Dim("running  "), p.running.addr, serveErr)
	default:
		say("%s nothing is answering on %s", ui.Dim("running  "), origin())
	}

	if err := p.proveReplacement(); err != nil {
		return nil, err
	}
	p.findDrift()
	return p, nil
}

// proveReplacement asks the installed daemon, out of process, whether it could
// take over. First, always: nothing is stopped until this passes.
//
// `dibs` links its own copy of core and could fold the ledger itself, but that
// measures the CLI's build, and the binary the service is about to start is
// exactly the thing in doubt.
func (p *plan) proveReplacement() error {
	step("checking that " + filepath.Base(p.installed) + " can rebuild this board")
	// THE ADDRESS THIS DAEMON ACTUALLY SERVES, because that is what the
	// replacement will be started with a few steps below.
	//
	// Without it, -check answered for the DEFAULT address while the replacement
	// was started on the real one, so everything address- and TLS-specific went
	// unasked: the proof and the thing proved were about different daemons.
	// Recovery retries this same binary rather than the previous build, so a
	// failure there leaves the fleet down.
	// #nosec G204 -- installed comes from daemonPath(), which resolves the
	// daemon beside this binary or on PATH; dir and addr are this machine's own
	// resolved data directory and the address its daemon registered.
	out, err := exec.Command(p.installed, checkArgs(p)...).CombinedOutput()
	switch {
	case err != nil && tooOldForCheck(out):
		// A refusal, and a different one, said in its own words.
		//
		// Reporting this as "cannot rebuild the board" would be a lie of exactly
		// the kind this command exists to prevent: the binary did not fail the
		// check, it is too old to be asked. Found on the first real run, where
		// the installed daemon predated this flag and the fleet was told its
		// board was unrebuildable.
		return fmt.Errorf("the dibd installed at %s is older than this dibs: it has no "+
			"-check, so it cannot prove it would rebuild this board.\n\n"+
			"  Nothing has been stopped. Moving a live fleet onto a daemon that cannot\n"+
			"  be verified is the one thing this command will not do blind.\n\n"+
			"  Install the matching daemon first (`task install`, or your package\n"+
			"  manager), then run `dibs upgrade` again", p.installed)
	case err != nil:
		return fmt.Errorf("%s cannot rebuild the board at %s, so nothing has been "+
			"stopped and the daemon now running is untouched:\n\n%s",
			p.installed, p.dir, strings.TrimSpace(string(out)))
	}
	say("  %s", strings.TrimSpace(string(out)))
	p.checked = checkedVersion(out)
	return nil
}

// findDrift decides what needs reconciling, and only for THIS board.
//
// unitDaemon() answers "is there a Dibs service on this machine", which is a
// different question and the wrong one here: an operator running an isolated
// second board (SECURITY.md) would have had their PRIMARY service repointed at
// the scratch directory they were upgrading. Caught on the first end-to-end
// run, which did exactly that to the production unit on this machine.
func (p *plan) findDrift() {
	if u := unitPinning(p.dir); u != "" {
		if unit, pinned := unitDaemon(); unit == u {
			p.unit, p.pinned = unit, pinned
		}
	}
	p.unitWrong = p.unit != "" && p.pinned != "" && !sameBinary(p.pinned, p.installed)
	p.moveDir = p.inherited != "" && p.opts.adoptDir
}

func (p *plan) report() error {
	step("dry run: nothing below was done")
	if p.unitWrong {
		say("  would rewrite %s, which pins %s", ui.Path(p.unit), ui.Path(p.pinned))
	}
	if p.moveDir {
		say("  would move %s to %s", ui.Path(p.inherited), ui.Path(adoptedName(p.inherited)))
	}
	if p.inherited != "" && !p.opts.adoptDir {
		say("  %s is named by an older version; --adopt-dir would move it", ui.Path(p.inherited))
	}
	say("  would restart the daemon and wait for it to serve again")
	return nil
}

// preflight fails everything that can fail while the daemon is still up.
//
// The first end-to-end run stopped the daemon and THEN discovered it could not
// write the unit, leaving a board with no daemon and an operator holding an
// error message. Anything checkable belongs here, and the two steps in cutover
// are the only ones that genuinely require the daemon to be down: a unit is
// read at load time, and a directory cannot be moved out from under the process
// holding its lock.
func (p *plan) preflight() error {
	if p.unitWrong || p.moveDir {
		if err := unitIsWritable(p.unit); err != nil {
			return fmt.Errorf("the service unit cannot be rewritten, so nothing has been "+
				"stopped: %w", err)
		}
		// AND THE REFUSALS THE WRITE ITSELF APPLIES, not only the file mode.
		//
		// unitIsWritable answers "can this file be opened for writing", and
		// writeServiceUnit refuses for a second reason that has nothing to do
		// with permissions: a unit under one of the legacy labels is still
		// installed, and writing the current one beside it would leave two jobs
		// on one data directory. `replaceUnits` waives the "refusing to
		// overwrite a unit you may have tuned" check and deliberately does not
		// waive that one.
		//
		// So an operator on a legacy-labelled unit passed preflight, had the
		// daemon stopped, and only then met a refusal that was knowable before
		// anything moved. Recovery restarted through that same legacy unit,
		// whose ExecStart still pins the OLD binary, and printed "This is the
		// NEW build, not a rollback": an upgrade that did not upgrade, saying it
		// did. That is the failure class this command has already shipped once.
		// Migrating exactly such an installation is ordinary use, which
		// reconcile's own comment says.
		//
		// Asked with replaceUnits set, because the question is what the REAL
		// write will do, and the real write sets it. Asking without it would
		// refuse on the existing current unit, which is the file upgrade exists
		// to rewrite.
		conflict := func() error {
			replaceUnits = true
			defer func() { replaceUnits = false }()
			return refuseIfUnitConflicts()
		}()
		if conflict != nil {
			return fmt.Errorf("the service unit cannot be rewritten, so nothing has been "+
				"stopped: %w", conflict)
		}
	}
	if p.moveDir {
		if _, err := os.Stat(adoptedName(p.inherited)); err == nil {
			return fmt.Errorf("%s already exists, so nothing has been stopped. Two data "+
				"directories are two boards; merging them is not something this command "+
				"will guess at", adoptedName(p.inherited))
		}
	}
	return nil
}

// cutover is the only phase that stops anything, and it is responsible for the
// daemon being up again however it ends.
func (p *plan) cutover() error {
	stopped := false
	// EITHER SIGNAL IS ENOUGH TO MEAN "SOMETHING IS RUNNING".
	//
	// p.serving is one request to the board, and p.running comes from the
	// registry the daemon writes: two independent pieces of evidence, and this
	// consulted only the first. Any transient failure of that request, a
	// timeout, a certificate hiccup, an address that resolved differently, made
	// serving false and skipped the stop entirely. The replacement then started,
	// exited at once on the directory lock the original still holds, and the
	// original went on answering: verification found a board, found no
	// pre-upgrade serial to compare it against, and printed `upgraded:` for the
	// process this command was supposed to replace. With --adopt-dir the data
	// directory is renamed under that live writer as well.
	//
	// REGISTERED BEFORE THE STOP IT COVERS. This block sat below the stop, so
	// the stop's own failure path returned before the defer existed: a SIGTERM
	// that landed but outran the wait left the daemon exiting with nothing
	// armed to restart it, while the error promised a restart. The test that
	// guarded it checked the order of two strings in the source and passed.
	// Found by the pre-release review, which ran that test to prove it.
	// A daemon this command stopped is a daemon it is responsible for starting,
	// including on the paths where something below goes wrong. Leaving a fleet
	// with no board and an error message is the worst outcome available here,
	// and it is worse than whatever failed.
	restored := false
	// The directory recovery must use is the one that EXISTS when it runs.
	//
	// This passed p.dir unconditionally, and reconcile below may have already
	// renamed the data directory: with --adopt-dir the recovery then pointed a
	// daemon at a path that had just been moved out from under it, while
	// printing that the board was unchanged. Tracked here and updated the
	// moment the move succeeds.
	recoverDir := p.dir
	defer func() {
		if !stopped || restored {
			return
		}
		p.recover(recoverDir)
	}()
	// A registered daemon is stopped whether or not it answered a moment ago.
	// UNKNOWN COUNTS AS RUNNING. Stopping a daemon that was not there costs a
	// no-op; skipping the stop because the registry was unreadable leaves the
	// old one serving while this command reports the new one. The asymmetry
	// decides it, and it is the same asymmetry as everywhere else in this file.
	if p.serving || p.running.addr != "" || p.running.unknown {
		step("stopping the daemon")
		stopErr := p.doStop(p.dir)
		// A STOP THAT TIMED OUT IS STILL A STOP.
		//
		// doStop sends SIGTERM and then waits. Returning early on the wait meant
		// treating a delivered signal as though nothing had happened: this
		// printed "could not stop the daemon, so nothing else was changed", the
		// daemon exited a few seconds later, launchd left it down because a
		// clean exit is not a crash, and the operator's board was gone. Measured
		// here, on this machine, with a 32-agent fleet.
		//
		// So the flag is set either way, which arms the recovery below and makes
		// this command responsible for putting a daemon back, exactly as the
		// comment under it says it must be.
		stopped = true
		if stopErr != nil {
			return fmt.Errorf("the daemon did not stop cleanly; the board will be "+
				"restarted rather than left down: %w", stopErr)
		}
	}

	// The directory FIRST, then the error. reconcile reports where the data
	// directory is now whether or not it finished, because the recovery below
	// has to start a daemon against something that exists.
	newDir, err := p.reconcile()
	if newDir != "" {
		recoverDir = newDir
	}
	if err != nil {
		return err
	}

	step("starting " + filepath.Base(p.installed))
	if err := p.doStart(newDir); err != nil {
		return err
	}
	// NOT here, and this is the whole guarantee.
	//
	// It was here, and the recovery could therefore never fire on the one
	// failure that matters: the daemon not coming back. Measured on a live
	// board, which stayed down while the command reported the failure and
	// exited. A start that returned no error is not a daemon that is serving:
	// `launchctl kickstart` exits 0 having merely SCHEDULED a spawn, and the
	// program it schedules can be missing. Only the board answering proves it.
	if err := p.doConfirm(newDir); err != nil {
		return err
	}
	restored = true
	return nil
}

// reconcile performs the two changes that require the daemon to be down, and
// reports the data directory the daemon should now start against.
func (p *plan) reconcile() (newDir string, err error) {
	newDir = p.dir
	unitWrong := p.unitWrong
	if p.moveDir {
		step("adopting the current data directory name")
		target := adoptedName(p.inherited)
		if err := os.Rename(p.inherited, target); err != nil {
			// Deliberately silent about the daemon: the deferred recovery in
			// cutover is bringing it back and says so on its own line. An error
			// that asserts "the daemon is stopped" beside a line reading "the
			// daemon was restarted" is two contradictory sentences in one
			// result, and the first one loses a fleet if believed. Measured:
			// this pair printed together on the first recovery test.
			return newDir, fmt.Errorf("could not move %s to %s, so the directory is "+
				"untouched and nothing was adopted: %w", p.inherited, target, err)
		}
		newDir = target
		say("  %s → %s", ui.Path(p.inherited), ui.Path(target))
		// The unit pins the OLD path as a literal argument, so it must be
		// rewritten whether or not it also pins the wrong binary. Skipping this
		// leaves a service that starts against a directory that is gone, which
		// is the exact failure `dibs doctor`'s own hint warns about.
		unitWrong = p.unit != ""
	}
	if !unitWrong {
		return newDir, nil
	}
	step("rewriting " + filepath.Base(p.unit))
	if err := os.Setenv("DIBS_DIR", newDir); err != nil {
		return newDir, err
	}
	replaceUnits = true
	defer func() { replaceUnits = false }()
	if err := writeServiceUnit(); err != nil {
		// newDir, NOT "". By here the directory may already have MOVED, and the
		// caller's recovery starts a daemon against whatever this reports. It
		// returned "" and the recovery then pointed at the vanished original,
		// which is the one moment in an upgrade when there is no board at all.
		// The failure is real (a legacy unit blocks the rewrite, which is
		// exactly the installation --adopt-dir exists to migrate), so this path
		// is reached by ordinary use rather than by accident.
		return newDir, fmt.Errorf("could not rewrite %s, so the service still pins %s: %w",
			p.unit, p.pinned, err)
	}
	return newDir, nil
}

// verify proves the board came back rather than asserting it.
//
// State is never at risk (state == fold(ledger)), but saying so is not the same
// as showing it, and "never let a user believe an update risks their state" is
// the requirement (R12). So: subtract.
func (p *plan) verify(newDir string) error {
	step("waiting for the board")
	after, err := waitForBoard(p.running.addr, 90*time.Second)
	if err != nil {
		return fmt.Errorf("the daemon did not start serving: %w\n\n"+
			"  The ledger is untouched and the board is still in it. Start the daemon\n"+
			"  in the foreground to see why: %s -dir %s", err, p.installed, newDir)
	}
	if p.serving && after.Serial < p.before.Serial {
		return fmt.Errorf("the board came back at serial %d, behind the %d it was "+
			"serving. Stop and report this: the ledger is intact and nothing has "+
			"been discarded", after.Serial, p.before.Serial)
	}
	if p.serving && after.Agents != p.before.Agents {
		say("  %s %d agent(s), was %d: a lease can lapse across a restart, and a "+
			"registered agent returns by re-registering with its nonce",
			ui.Dim("note"), after.Agents, p.before.Agents)
	}
	// SAY WHAT WAS MEASURED. verify compares a serial and a count, and nothing
	// about identity or registration events, so "no agent had to re-register"
	// is a claim this code cannot make. It also printed unconditionally, two
	// lines under a note explaining that a lease MAY have lapsed and an agent
	// returns by re-registering: the same paragraph both warned of it and
	// denied it. Found by a pre-release review.
	say("%s serial %d, %d agent(s)", ui.Bold("upgraded:"), after.Serial, after.Agents)
	if p.inherited != "" && !p.opts.adoptDir {
		say("%s %s is named by an older version. Nothing is wrong; `dibs upgrade "+
			"--adopt-dir` moves it and repoints the service in one step",
			ui.Dim("note"), ui.Path(p.inherited))
	}
	return nil
}

// adoptedName is the current spelling of an inherited data directory, beside it.
func adoptedName(inherited string) string {
	return filepath.Join(filepath.Dir(inherited), ".dibs")
}

// unitUnfitToRestart reports why a service unit must not be used to bring the
// daemon back, or "" when it is fit. Two ways it is unfit, each of which
// turned a recovery into a quiet lie once:
//
//   - it names the wrong DATA DIRECTORY: --adopt-dir renamed the directory and
//     the unit rewrite failed, so restarting the unit ran against a path moved
//     out from under it.
//   - it pins the wrong BINARY: recovery starts the build just installed and
//     the report says "This is the NEW build", but a unit whose ExecStart still
//     names the old binary (a legacy-labelled unit a failed stop never let
//     reconcile rewrite) brings the OLD daemon back and is called the new one.
//
// The directory check has been here since round ten; the binary check since
// round sixty-two, both found by the pre-release review. "" for a binary it
// cannot read out of the unit, matching unitNames: a doubt does not justify
// abandoning a supervised service.
func unitUnfitToRestart(unit, dir, installed string) string {
	if !unitNames(unit, dir) {
		return unit + " does not name " + dir
	}
	if bin := unitBinary(unit); bin != "" && !sameBinary(bin, installed) {
		return unit + " pins " + bin + ", not the " + installed + " just installed"
	}
	return ""
}

// unitBinary is the dibd path a service unit pins, or "" when the unit
// cannot be read or names none. "" means "cannot tell", and the caller then
// leaves the unit in place rather than force a direct start over a doubt.
func unitBinary(unit string) string {
	// #nosec G304,G703 -- unit is a service-unit path this project built from
	// the process's own HOME (or XDG_CONFIG_HOME) plus a fixed filename, or one
	// the operator passed for their own machine; read and matched, never
	// written, exactly as unitNames does above.
	b, err := os.ReadFile(unit)
	if err != nil {
		return ""
	}
	body := string(b)
	// BY DIRECTIVE, NOT BY SHAPE, and this is the third round it took.
	//
	// Round sixty-seven matched the daemon's name with a regex, which cut a
	// path containing a space in half. Round sixty-eight took the first token
	// that looked like an absolute path ending in `dibd`, which reads a
	// unit's OTHER directives too: `WorkingDirectory=/srv/dibd` above an
	// ExecStart naming `/opt/dibs/bin/dibd` answered with the working
	// directory. Then findDrift calls a correct unit wrong and reconcile
	// rewrites it, discarding whatever the operator had tuned there. Both
	// cuts were pattern matching over a file with a grammar. This reads the
	// key that actually names the executable, in each of the two formats this
	// project writes. Found by the pre-release review, round seventy-eight.
	if i := strings.Index(body, "ProgramArguments"); i >= 0 {
		// launchd: the first <string> of the array that follows the key is the
		// program; the rest are its arguments.
		if m := plistString.FindStringSubmatch(body[i:]); m != nil {
			return html.UnescapeString(m[1])
		}
		return ""
	}
	for _, line := range strings.Split(body, "\n") {
		v, ok := strings.CutPrefix(strings.TrimSpace(line), "ExecStart=")
		if !ok {
			continue
		}
		// systemd allows prefix characters on the command (`-` to ignore a
		// failure, `@`, `+`, `!`); none of them are part of the path.
		if toks := systemdTokens(strings.TrimLeft(v, "-@+!:")); len(toks) > 0 {
			return toks[0]
		}
	}
	return ""
}

// startDaemon brings the daemon back, through the service manager when there is
// a unit and directly when there is not.
//
// The unit is preferred wherever it exists, because a daemon this command
// started by hand would not survive the operator's next logout, and an upgrade
// that quietly downgrades a supervised service to an orphan process is a worse
// outcome than the drift it was fixing.
func startDaemon(installed, dir, unit string, was daemonState) error {
	// UNLESS THE UNIT NAMES A DIFFERENT BOARD.
	//
	// --adopt-dir renames the data directory and rewrites the unit to match. If
	// that rewrite fails, recovery ran here with the CORRECT new directory in
	// `dir` and started the unit anyway, which still pointed at the path that
	// had just been moved out from under it: a daemon started against a
	// directory that no longer exists, reported as a recovery.
	//
	// Preferring the unit is right when it describes this board and wrong when
	// it does not, and the file says which.
	if unit != "" {
		if reason := unitUnfitToRestart(unit, dir, installed); reason != "" {
			fmt.Fprintf(os.Stderr, "%s %s, so the daemon is being started directly with the "+
				"installed binary rather than through it. Fix the unit before the next logout, "+
				"or the board will not come back as the new build on its own.\n",
				ui.Bold("note:"), reason)
			unit = ""
		}
	}
	if unit != "" {
		err := restartUnit(unit)
		if err == nil {
			return nil
		}
		if !errors.Is(err, errNoServiceManager) {
			return fmt.Errorf("could not restart the service: %w", err)
		}
		// Fall through: a unit file on disk that the service manager does not
		// know about is a unit that was never loaded, and refusing here would
		// leave the fleet with no daemon over a bookkeeping detail.
	}
	args := []string{"-dir", dir}
	// The SAME address the preflight was given, transport included: the proof
	// and the thing proved have to describe one daemon, and the restart is the
	// thing proved.
	if a := replacementAddr(dir, was.addr); a != "" {
		args = append(args, "-addr", a)
	}
	// A second board on one machine is a deliberate configuration (isolating
	// agents you do not trust, SECURITY.md), and it is refused by default. A
	// daemon that was running alongside others has to be allowed to again, or
	// the restart fails on a rule the operator already answered.
	if was.parallel {
		args = append(args, "-allow-parallel")
	}
	// #nosec G204 -- installed is resolved by daemonPath(); dir is the resolved
	// data directory; addr comes from the daemon registry this machine wrote.
	cmd := exec.Command(installed, args...)
	cmd.Stdout, cmd.Stderr = nil, nil
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start %s: %w", installed, err)
	}
	// An exit within the first moment is a start that failed, and the usual
	// one is the directory lock a daemon that has not finished stopping still
	// holds. This used to reap the process in the background and report the
	// start as done. Found by the pre-release review, round ten.
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		if err != nil {
			return fmt.Errorf("%s exited at once: %w", filepath.Base(installed), err)
		}
		return nil
	case <-time.After(startExitWatch):
		return nil
	}
}

var errNoServiceManager = errors.New("no service manager knows this unit")

// restartUnit brings a unit back, always RELOADING it first.
//
// launchd reads a plist at LOAD time and holds the parsed definition in memory,
// so rewriting the file changes nothing a running launchd knows: `kickstart`
// then restarts the OLD definition, exits 0, and schedules a spawn of a program
// that may not exist. Measured on a live board, which is how this was found: the
// plist on disk correctly named the new daemon while `launchctl print` still
// showed `program = ~/go/bin/dibd` and `active count = 0`.
//
// systemd is the same shape with a different spelling: a changed unit needs
// `daemon-reload` before `restart`, or the old ExecStart is what runs.
//
// Unconditional, not "when we rewrote it". The first version reloaded only on
// its own rewrites, which misses every OTHER way the loaded definition drifts
// from the file: a plist edited by hand, one rewritten by an earlier run that
// then failed, one changed by a package upgrade. All of those present
// identically, as a service that starts the wrong program while the file on
// disk reads correctly, and the machine this was written on was in exactly that
// state. Reloading is idempotent and costs a moment, so paying it every time is
// cheaper than being right only about the cause we happened to know about.
func restartUnit(unit string) error {
	if err := reloadUnit(unit); err != nil {
		return err
	}
	return kickstartUnit(unit)
}

// reloadUnit makes the service manager re-read the file on disk.
func reloadUnit(unit string) error {
	switch runtime.GOOS {
	case "darwin":
		domain := fmt.Sprintf("gui/%d", os.Getuid())
		// bootout may legitimately fail with "not loaded", which is the state
		// bootstrap wants anyway: the only thing that matters is the outcome.
		// #nosec G204 -- unit is resolved from this machine's own unit paths
		_ = exec.Command("launchctl", "bootout", domain+"/"+
			strings.TrimSuffix(filepath.Base(unit), ".plist")).Run()
		// #nosec G204 -- unit is resolved from this machine's own unit paths
		out, err := exec.Command("launchctl", "bootstrap", domain, unit).CombinedOutput()
		if err != nil {
			return fmt.Errorf("launchctl bootstrap %s: %w: %s", unit, err, strings.TrimSpace(string(out)))
		}
		return nil
	case "linux":
		out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemctl --user daemon-reload: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	return errNoServiceManager
}

// kickstartUnit restarts a loaded unit through its own service manager.
func kickstartUnit(unit string) error {
	switch runtime.GOOS {
	case "darwin":
		label := strings.TrimSuffix(filepath.Base(unit), ".plist")
		// kickstart -k restarts a running service and starts a stopped one, which
		// is what makes this safe to run whatever state the daemon was in.
		target := fmt.Sprintf("gui/%d/%s", os.Getuid(), label)
		// #nosec G204 -- label derived from our own unit filename
		out, err := exec.Command("launchctl", "kickstart", "-k", target).CombinedOutput()
		if err == nil {
			return nil
		}
		if strings.Contains(string(out), "Could not find service") {
			return errNoServiceManager
		}
		return fmt.Errorf("launchctl kickstart %s: %w: %s", target, err, strings.TrimSpace(string(out)))
	case "linux":
		name := strings.TrimSuffix(filepath.Base(unit), ".service")
		// #nosec G204 -- name derived from our own unit filename
		out, err := exec.Command("systemctl", "--user", "restart", name).CombinedOutput()
		if err == nil {
			return nil
		}
		if strings.Contains(string(out), "not found") || strings.Contains(string(out), "not loaded") {
			return errNoServiceManager
		}
		return fmt.Errorf("systemctl --user restart %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return errNoServiceManager
}

// fleet is the little of the board this command compares across a restart.
type fleet struct {
	Serial uint64
	Agents int
}

// fleetSnapshot reads the public board, or reports that nothing is serving.
//
// Through the same `get` every other verb uses, so it authenticates and pins
// the certificate the same way, and a remote board is read the same as a local
// one rather than through a second half-built client.
func fleetSnapshot() (fleet, error) { return fleetSnapshotAt("") }

// fleetSnapshotAt reads the board of the daemon at a DISCOVERED address.
//
// The address matters because upgrade already goes to the trouble of finding
// it: the registry records what each live daemon bound, so a board serving on a
// LAN address is not restarted on loopback. Both the before-snapshot and the
// verification then called the address-free form, which asks this CLI's own
// environment and config, so the proof that "the board came back" was collected
// from whichever daemon that named. With two boards up it stopped one and read
// the other, then reported success. With one board bound to an address the CLI
// does not know, it restarts correctly and reports a failure that did not
// happen. Found by the pre-release review, with a reproduction.
func fleetSnapshotAt(addr string) (fleet, error) {
	var b boardView
	if err := getAt(originFor(addr), "/api/board", &b); err != nil {
		return fleet{}, err
	}
	return fleet{Serial: b.Serial, Agents: len(b.Agents)}, nil
}

// waitForBoard blocks until the daemon serves the board again.
//
// Bounded, because "never came back" has to surface as an error somebody can
// act on rather than a hang, and it waits on the BOARD rather than on /livez:
// liveness answers before replay finishes, and an upgrade that reported success
// while the board was still rebuilding would be reporting the wrong thing.
func waitForBoard(addr string, limit time.Duration) (fleet, error) {
	deadline := time.Now().Add(limit)
	var last error
	for {
		f, err := fleetSnapshotAt(addr)
		if err == nil {
			return f, nil
		}
		last = err
		if time.Now().After(deadline) {
			return f, fmt.Errorf("nothing served the board on %s within %s: %w",
				originFor(addr), limit, last)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// tooOldForCheck reports a daemon that predates -check, as opposed to one that
// ran the check and failed it.
//
// Split out because the difference decides what the operator is told, and the
// two are one character apart in the exec result: both are a non-zero exit with
// output. Getting it wrong tells a fleet its board cannot be rebuilt when the
// board is fine and the binary is simply old, which is what the first real run
// of this command did.
func tooOldForCheck(out []byte) bool {
	return strings.Contains(string(out), "not defined: -check")
}

// daemonState is how the daemon was running before this command stopped it, so
// it can be started the same way.
type daemonState struct {
	addr     string
	parallel bool
	// unknown means the registry could not be READ, which is not the same as
	// nothing running.
	//
	// LiveDaemons says so in its own comment: "conflating the two is how a
	// guard fails open". This returned a zero daemonState on a read error, so
	// an unreadable registry read as "no daemon here", the stop was skipped,
	// the replacement exited at once on the directory lock the original still
	// holds, and the original went on answering. Verification then found a
	// board, had no pre-upgrade serial to compare against, and printed
	// `upgraded:` for the process this command exists to replace. Found by the
	// pre-release review, which noted the adjacent comment already forbids it.
	unknown bool
}

// runningDaemon reads the registry for the daemon serving dir, and notes
// whether it was sharing this machine with others.
//
// Best-effort by design: a registry that cannot be read means starting with
// defaults, which is what happens today anyway. Silence here must not stop an
// upgrade, because the alternative is an operator left with a stopped daemon
// and a command that refused to finish.
func runningDaemon(dir string) daemonState {
	live, err := paths.LiveDaemons()
	if err != nil {
		return daemonState{unknown: true}
	}
	mine, others, err := selectDaemon(live, dir)
	if err != nil || mine == nil {
		return daemonState{parallel: others > 0}
	}
	// WITH THE SCHEME IT WAS ASKED FOR. The registry records the bare
	// listener and, when the daemon was told a transport on its flag or in
	// DIBS_ADDR, that transport; handed back together, replacementAddr
	// passes the stated form through untouched, and the replacement is the
	// daemon that was running. Found by the pre-release review, round
	// thirty-nine.
	addr := mine.Addr
	if mine.Scheme != "" && addr != "" {
		addr = mine.Scheme + "://" + addr
	}
	return daemonState{addr: addr, parallel: others > 0}
}

// systemdTokens splits a unit the way systemd does, and reverses what
// systemdArg escaped.
//
// The inverse of systemdArg, and it has to stay that way: double quotes group,
// a backslash escapes the next character, and `%%` and `$$` are systemd's own
// doubling for a literal percent and dollar. Reading the file with a plain
// field split is what made a board with a space in its path invisible to the
// command that upgrades it.
func systemdTokens(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		rs := []rune(strings.TrimSpace(line))
		var cur strings.Builder
		inQuote, started := false, false
		flush := func() {
			if started {
				out = append(out, undouble(cur.String()))
				cur.Reset()
				started = false
			}
		}
		for i := 0; i < len(rs); i++ {
			switch r := rs[i]; {
			case r == '\\' && i+1 < len(rs):
				i++
				cur.WriteRune(rs[i])
				started = true
			case r == '"':
				inQuote = !inQuote
				started = true // `""` is an empty argument, not nothing
			case !inQuote && (r == ' ' || r == '\t'):
				flush()
			default:
				cur.WriteRune(r)
				started = true
			}
		}
		flush()
	}
	return out
}

// undouble reverses systemd's own escaping for a literal percent and dollar.
func undouble(s string) string {
	s = strings.ReplaceAll(s, "%%", "%")
	return strings.ReplaceAll(s, "$$", "$")
}

// unitIsWritable answers, before anything is stopped, whether the unit rewrite
// that comes after the stop can actually happen.
//
// Cheap and worth it: the rewrite is the one step between stopping the daemon
// and starting it again that touches a file this process may not own, and
// discovering that afterwards is how the first run of this command ended with a
// stopped daemon and no service.
func unitIsWritable(unit string) error {
	if unit == "" {
		return nil
	}
	// #nosec G304 -- resolved from this machine's own unit paths
	f, err := os.OpenFile(unit, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("%s: %w", unit, err)
	}
	return f.Close()
}

// checkArgs is what `dibd -check` is asked, separated so a test can read it.
//
// The argv IS the defect: it named only the directory, while the replacement is
// started with the address the running daemon bound. A test that ran the whole
// upgrade could not isolate that, and one that restated the list would not
// notice it changing.
func checkArgs(p *plan) []string {
	args := []string{"-check", "-dir", p.dir}
	if a := replacementAddr(p.dir, p.running.addr); a != "" {
		args = append(args, "-addr", a)
	}
	return args
}

// replacementAddr is the address to hand the replacement daemon, WITH the
// transport the board is actually serving.
//
// The registry stores what net.Listen was given, and resolveListenAddr strips
// any scheme before the daemon registers, so `addr` alone is a bare host:port.
// Handing that back means the replacement re-infers a transport from the host:
// an http:// LAN board is checked and restarted as HTTPS, and an https://
// loopback board becomes plaintext. The preflight then approves a different
// daemon than the one it is about to authorise stopping, and the restart can
// leave every existing client unable to reconnect.
//
// The scheme is not in the registry, and it does not need to be: the data
// directory's own configuration is what the daemon resolved it from, and asking
// the shared resolver is the same question the daemon will ask on the way up.
func replacementAddr(dir, addr string) string {
	if addr == "" {
		return ""
	}
	if _, _, found := strings.Cut(addr, "://"); found {
		return addr // already stated, nothing to add
	}
	// ONLY WHEN THE CONFIG DESCRIBES THIS ADDRESS.
	//
	// resolveTransport answers for the address the CONFIG names, and the
	// running daemon may be on a different one: started with `-addr
	// 0.0.0.0:4777` against a config that names nothing, it resolves loopback
	// and returns http, so a TLS wildcard board would be handed
	// `http://0.0.0.0:4777` and restarted in plaintext. Losing the scheme is
	// bad; asserting the wrong one is worse, because the daemon can still
	// re-resolve correctly from a bare address and cannot recover from a lie.
	//
	// So the scheme is added only when the config is talking about the same
	// address the daemon actually bound. Otherwise the bare form goes through
	// and the replacement resolves it exactly as the original did.
	// DIBS_ADDR FIRST, BECAUSE THE DAEMON READS IT FIRST.
	//
	// resolveListenAddr takes -addr, then DIBS_ADDR, then the config. This
	// command passes -addr, which OUTRANKS the variable still set in the
	// environment the replacement inherits: a board launched with
	// DIBS_ADDR=http://10.0.0.9:4777 whose dibs.toml does not repeat that
	// address was handed a bare `10.0.0.9:4777`, and the replacement re-inferred
	// TLS for a non-loopback host while every client went on speaking plaintext.
	// The reverse turns an explicitly plaintext loopback board into one.
	//
	// Same rule as the config below: state the scheme only where the source is
	// talking about the listener the daemon actually bound.
	if env := os.Getenv("DIBS_ADDR"); sameHostPort(env, addr) {
		if scheme, _, found := strings.Cut(env, "://"); found {
			switch strings.ToLower(scheme) {
			case "http", "https":
				return strings.ToLower(scheme) + "://" + addr
			}
		}
	}
	configured, cerr := readConfiguredAddr(paths.DataDir())
	if cerr != nil || !sameHostPort(configured, addr) {
		return addr
	}
	scheme, _, err := resolveTransport(dir)
	if err != nil || scheme == "" {
		return addr
	}
	return scheme + "://" + addr
}

// sameHostPort reports whether two addresses name the same listener, ignoring
// any scheme on either side.
func sameHostPort(a, b string) bool {
	return bare(a) != "" && bare(a) == bare(b)
}

// bare strips a scheme, so "https://h:1" and "h:1" compare equal.
func bare(a string) string {
	if _, rest, found := strings.Cut(a, "://"); found {
		return rest
	}
	return a
}

// unitNames reports whether a service unit refers to this data directory.
//
// Read rather than assumed: the one case that matters is a unit whose rewrite
// failed after the directory moved, and the only evidence for that is the file
// itself. An unreadable unit is treated as naming it, because refusing to use a
// unit we cannot read would downgrade a supervised service to an orphan process
// over a permissions problem, which is the worse outcome this function's own
// comment already argues.
//
// TOKENS, NOT A SUBSTRING, and the first version was wrong in both directions.
//
// A plist is XML, so a board at `.../Fleet & Review` is written `Fleet &amp;
// Review` and a raw search rejected the unit as somebody else's: a supervised
// daemon quietly demoted to a direct start because its path contained an
// ampersand. And `strings.Contains` accepted `~/.dibs-old` as naming `~/.dibs`,
// which is exactly the wrong-board case the check exists to catch, and the
// likeliest spelling of it after an --adopt-dir rename.
//
// So the file is split into candidate values, unescaped, and compared as whole
// paths.
func unitNames(unit, dir string) bool {
	// #nosec G304,G703 -- `unit` is a service-unit path this project builds from
	// the process's own HOME (or XDG_CONFIG_HOME) plus a fixed filename, or one
	// the operator passed for their own machine. It is read and compared, never
	// written, and unitPinning is the caller that made the taint visible.
	b, err := os.ReadFile(unit)
	if err != nil {
		return true
	}
	want := map[string]bool{filepath.Clean(dir): true}
	if abs, aerr := filepath.Abs(dir); aerr == nil {
		want[filepath.Clean(abs)] = true
	}
	for _, tok := range unitTokens(string(b)) {
		if want[filepath.Clean(tok)] {
			return true
		}
	}
	return false
}

// unitTokens pulls the candidate paths out of a service unit.
//
// Both shapes this project writes: a launchd plist, where values sit in
// <string> elements and carry XML entities, and a systemd unit, where an
// ExecStart line is whitespace-separated. Splitting on both and unescaping is
// cheaper than parsing either properly, and the comparison afterwards is exact,
// so a stray token costs nothing.
func unitTokens(body string) []string {
	var out []string
	// A plist <string> is ONE value, spaces included: a board at `.../Fleet &
	// Review` is a single path, and splitting on whitespace turned it into
	// three tokens that match nothing. Taken whole, then unescaped.
	for _, m := range plistString.FindAllStringSubmatch(body, -1) {
		out = append(out, html.UnescapeString(m[1]))
	}
	// And a systemd unit is parsed the way systemd parses it, because this
	// project WRITES it with systemdArg and could not read its own output.
	// Splitting on quotes as if they were separators turned `-dir "/tmp/Fleet
	// Review"` into `/tmp/Fleet` and `Review`, neither of which matches the
	// board, so the unit describing this very daemon read as another board's:
	// upgrade then started a detached process instead of the service, and
	// systemd stopped supervising it across logout and reboot. It printed a
	// warning and accepted that. Same for any path holding `%`, `$` or a
	// backslash, which systemdArg doubles and this never undid.
	for _, chunk := range systemdTokens(body) {
		if chunk != "" {
			out = append(out, html.UnescapeString(chunk))
		}
	}
	return out
}

// plistString matches one <string> element's contents, including spaces.
var plistString = regexp.MustCompile(`(?s)<string>(.*?)</string>`)
