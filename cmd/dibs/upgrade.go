package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	running            daemonState
	before             fleet
	serving            bool
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
	return p.cutover()
}

// planUpgrade resolves what is out of line, and proves the replacement can
// rebuild the board. Nothing here changes anything.
func planUpgrade(o upgradeOpts) (*plan, error) {
	p := &plan{opts: o}
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
	p.before, serveErr = fleetSnapshot()
	p.serving = serveErr == nil
	if p.serving {
		say("%s serial %d, %d agent(s)", ui.Dim("running  "), p.before.Serial, p.before.Agents)
	} else {
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
	if p.serving {
		step("stopping the daemon")
		if err := stopDaemon(p.dir); err != nil {
			return fmt.Errorf("could not stop the daemon, so nothing else was changed: %w", err)
		}
		stopped = true
	}
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
		if err := startDaemon(p.installed, recoverDir, p.unit, p.running); err != nil {
			fmt.Fprintf(os.Stderr, "\n%s could not restart the daemon after the failure "+
				"above: start it with `%s -dir %s`: %v\n",
				ui.Bold("AND:"), p.installed, recoverDir, err)
			return
		}
		// SAY WHICH BUILD. This read "restarted on the build it was already
		// running", which was never something this code could deliver: the
		// upgrade replaces the binary in place and keeps no copy of the old one,
		// so p.installed is the REPLACEMENT, and on the failure where the
		// replacement is what is wrong this restarted the thing that had just
		// failed and reported a rollback. An operator reading that goes looking
		// for a different cause.
		//
		// Getting a board back up is still the right move, and it is what this
		// does; the sentence now matches it. A recovery that puts the previous
		// build back needs one to put back, which means keeping a copy across
		// the replacement, and that is a change to how upgrade installs rather
		// than a wording fix.
		// "started", not "serving". startDaemon schedules or launches a process
		// and verifies no board, so claiming the board is back is a claim this
		// code cannot make. `dibs doctor` is what answers it.
		fmt.Fprintf(os.Stderr, "\n%s the daemon was started again from %s on %s. This "+
			"is the NEW build, not a rollback, and a start is not a serving board: "+
			"run `dibs doctor` to confirm. If the failure above was the new build "+
			"itself, install the previous one before relying on it.\n",
			ui.Bold("recovered:"), recoverDir, filepath.Base(p.installed))
	}()

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
	if err := startDaemon(p.installed, newDir, p.unit, p.running); err != nil {
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
	if err := p.verify(newDir); err != nil {
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
	after, err := waitForBoard(90 * time.Second)
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
	if unit != "" && !unitNames(unit, dir) {
		fmt.Fprintf(os.Stderr, "%s %s does not name %s, so the daemon is being "+
			"started directly rather than through it. Fix the unit before the next "+
			"logout, or the board will not come back on its own.\n",
			ui.Bold("note:"), unit, dir)
		unit = ""
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
	// Nothing waits on this process, so release it rather than leaving a zombie
	// for as long as this CLI lives.
	go func() { _ = cmd.Wait() }()
	return nil
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
func fleetSnapshot() (fleet, error) {
	var b boardView
	if err := get("/api/board", &b); err != nil {
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
func waitForBoard(limit time.Duration) (fleet, error) {
	deadline := time.Now().Add(limit)
	var last error
	for {
		f, err := fleetSnapshot()
		if err == nil {
			return f, nil
		}
		last = err
		if time.Now().After(deadline) {
			return f, fmt.Errorf("nothing served the board on %s within %s: %w",
				origin(), limit, last)
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
		return daemonState{}
	}
	mine, others, err := selectDaemon(live, dir)
	if err != nil || mine == nil {
		return daemonState{parallel: others > 0}
	}
	return daemonState{addr: mine.Addr, parallel: others > 0}
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
func unitNames(unit, dir string) bool {
	b, err := os.ReadFile(unit) // #nosec G304 -- the operator's own unit path
	if err != nil {
		return true
	}
	if abs, aerr := filepath.Abs(dir); aerr == nil {
		if strings.Contains(string(b), abs) {
			return true
		}
	}
	return strings.Contains(string(b), dir)
}
