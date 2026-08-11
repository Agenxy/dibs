package paths

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// The host-wide register of running daemons.
//
// Lives here rather than in cmd/dibd because two binaries need it for
// opposite reasons: dibd WRITES an entry and refuses to start beside a
// stranger, and `dibs doctor` READS them to tell an operator their fleet is
// split. A guard nothing can report on is half a feature when the failure it
// prevents is silent.
//
// # Liveness is a held lock, not a pid
//
// The first version asked kill(pid, 0). Pids are reused: a stale file whose pid
// had been reassigned to any other process this user owns reads as a live daemon
// forever, which turns the guard into a permanent refusal to start, and nothing
// sweeps it, because the sweep is the thing being fooled.
//
// So each daemon holds an exclusive flock on its own registration file for its
// entire life. Liveness becomes "somebody still holds this lock", which the
// kernel answers truthfully and releases on exit, crash or kill. A file whose
// lock is free is a corpse by definition and is removed.
//
// # One lock covers the whole decision
//
// Check-then-register is a TOCTOU: both daemons read an empty registry, both
// register, both start. So reading the registry, applying policy and writing an
// entry all happen while holding .hostlock. Readers take it too, which is also
// what makes a torn read impossible: a writer cannot be mid-write while a
// reader holds it.
//
// # Where it lives, and why not TempDir
//
// os.TempDir() honours $TMPDIR, which differs between a login shell, a launchd
// job and a sandbox. Two daemons with different TMPDIR values would take
// unrelated .hostlock files, see empty registries, and both start: the exact
// partition, restored by an environment variable. So the location is derived
// from the user's home directory, which is stable across launch contexts.
// Staleness is not a concern in a persistent directory because liveness is a
// held lock: yesterday's files are corpses and are swept on sight.

// Daemon is one running dibd, as it registered itself.
type Daemon struct {
	PID  int    `json:"pid"`
	Addr string `json:"addr"`
	Dir  string `json:"dir"`
	// Unknown marks an entry whose lock is held but whose contents could not be
	// decoded. Somebody is running something; we just cannot say what. It is a
	// field rather than an omission because omitting it was fail-open: a
	// zero-valued Daemon has an empty Dir, which canonicalises to the working
	// directory and could therefore match OUR directory, quietly removing an
	// unidentified live daemon from the list of strangers.
	Unknown bool `json:"-"`
}

// IsStranger reports whether this daemon is a different one from the daemon
// rooted at ourDir. An entry we could not decode is always a stranger, because
// the alternative is assuming it is us.
func (d Daemon) IsStranger(ourDir string) bool {
	// BOTH sides are canonicalised. This canonicalised only the registered
	// directory and compared it against whatever the caller passed, so a caller
	// holding an uncanonical path. DataDir() returns $DIBS_DIR verbatim, and
	// on macOS /tmp is a symlink to /private/tmp: found that every daemon was
	// a stranger, including itself.
	//
	// Both readings of that are bad. `dibs stop` concludes nothing is running
	// and stops nothing while reporting success; the parallel-daemon guard
	// concludes it is alone and lets a second daemon onto a directory the flock
	// then has to refuse. Canonical is idempotent, so doing it twice costs
	// nothing and removes the requirement that every caller remembers.
	return d.Unknown || Canonical(d.Dir) != Canonical(ourDir)
}

// RunRegistryDir is where daemons register themselves.
func RunRegistryDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".dibs-run")
	}
	// No home directory is pathological; fall back rather than refuse, and
	// accept that two daemons with different TMPDIR values could miss each other
	// in that case.
	return filepath.Join(os.TempDir(), "agents-run-"+strconv.Itoa(os.Getuid()))
}

// hostLock serialises the whole check-and-claim across every daemon for this
// user, so two starting together cannot both conclude they are alone.
func hostLock() (release func(), err error) {
	if err := os.MkdirAll(RunRegistryDir(), 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(RunRegistryDir(), ".hostlock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- our own registry dir
	if err != nil {
		return nil, err
	}
	// Blocking, not LOCK_NB: the critical section is a directory read and one
	// small write, so waiting is measured in microseconds, and failing here
	// because somebody else is mid-claim would reintroduce the race.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// LiveDaemons returns the daemons currently running.
//
// An error means the registry could not be read, which is NOT the same as
// "nothing is running": conflating the two is how a guard fails open. Callers
// that must not guess (the daemon deciding whether to start) have to treat the
// error as a refusal; callers that are only reporting (doctor) may say so.
func LiveDaemons() ([]Daemon, error) {
	unlock, err := hostLock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	return liveDaemonsLocked()
}

// liveDaemonsLocked is LiveDaemons with the host lock already held.
func liveDaemonsLocked() ([]Daemon, error) {
	entries, err := os.ReadDir(RunRegistryDir())
	if err != nil {
		return nil, fmt.Errorf("reading the daemon registry: %w", err)
	}
	var live []Daemon
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(RunRegistryDir(), name)
		d, alive, err := readRegistration(path)
		if err != nil {
			// Could not determine whether anyone holds it. Report rather than
			// assume in either direction.
			return nil, fmt.Errorf("inspecting %s: %w", path, err)
		}
		if !alive {
			_ = os.Remove(path) // its owner released the lock; nobody is running it
			continue
		}
		live = append(live, d)
	}
	return live, nil
}

// readRegistration decodes one entry and reports whether its owner still holds
// the lock on it.
//
// Called only with the host lock held, so no writer can be mid-write. A file
// that is held but undecodable is still reported as a live daemon: with
// whatever fields survived, because "somebody is running something here" is
// the fact that matters, and treating it as absent would be the fail-open
// answer.
func readRegistration(path string) (Daemon, bool, error) {
	alive, err := held(path)
	if err != nil {
		return Daemon{}, false, err
	}
	return describe(path), alive, nil
}

// describe reads one entry, or marks it Unknown.
//
// A read or decode failure is an OBSERVATION, not an error to propagate: the
// lock already told us somebody is running something here, and that is the fact
// the guard acts on. Returning an error instead would abort the whole scan over
// one damaged file and take the working entries down with it.
func describe(path string) Daemon {
	body, err := os.ReadFile(path) // #nosec G304 -- our own registry directory
	if err != nil {
		return Daemon{Unknown: true}
	}
	var d Daemon
	if json.Unmarshal(body, &d) != nil {
		return Daemon{Unknown: true}
	}
	return d
}

// held reports whether some process still holds the exclusive lock on a file.
//
// Tries to take the lock without blocking: success means it was free, so the
// owner is gone, and it is released immediately, because acquiring it was a
// question rather than a claim.
//
// Only contention means "held". Any other error is returned, because a
// filesystem that cannot lock is not evidence about who is running: reporting
// it as held would strand the entry forever, and as free would delete a live
// daemon's registration.
func held(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0o600) // #nosec G304 -- our own registry directory
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer func() { _ = f.Close() }()
	switch err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); {
	case err == nil:
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return false, nil
	case errors.Is(err, syscall.EWOULDBLOCK): // EAGAIN: somebody holds it
		return true, nil
	default:
		return false, err
	}
}

// Strangers is returned when another daemon is already running and parallel
// daemons were not permitted. It carries who, so the caller can say so.
type Strangers struct{ Others []Daemon }

func (s *Strangers) Error() string {
	return fmt.Sprintf("%d other dibd process(es) already running", len(s.Others))
}

// Claim atomically decides whether this daemon may start and, if so, registers
// it.
//
// One host lock covers reading the registry and writing our own entry, so two
// daemons racing cannot both find themselves alone.
//
// The policy is a BOOLEAN, not a callback. An earlier version invoked
// caller-supplied code while holding .hostlock, which is a deadlock waiting to
// be written: any callback that consulted the registry: the obvious thing for
// a policy to want: would block forever on a lock its own caller holds. Nothing
// but this file's own code runs inside the critical section now.
//
// Any failure to establish the picture is an error, never an empty list. The
// difference between "nothing is running" and "I could not find out" is the
// whole value of the guard.
//
// The returned release function drops the registration AND the lifetime lock.
func Claim(d Daemon, allowParallel bool) (release func(), err error) {
	unlock, err := hostLock()
	if err != nil {
		return nil, fmt.Errorf("cannot take the host registry lock: %w", err)
	}
	defer unlock()

	ourDir := Canonical(d.Dir)
	d.Dir = ourDir
	running, err := liveDaemonsLocked()
	if err != nil {
		return nil, err
	}
	var others []Daemon
	for _, o := range running {
		// Canonical, not raw string equality: two spellings of one directory are
		// the same daemon, and two different directories can share a relative
		// spelling from different working directories. An undecodable entry is a
		// stranger by definition.
		if o.IsStranger(ourDir) {
			others = append(others, o)
		}
	}
	if len(others) > 0 && !allowParallel {
		return nil, &Strangers{Others: others}
	}

	path := filepath.Join(RunRegistryDir(), strconv.Itoa(d.PID)+".json")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- our own registry dir
	if err != nil {
		return nil, err
	}
	// Held for the process lifetime: this is what makes us observably alive.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("another process already holds %s: %w", path, err)
	}
	body, err := json.Marshal(d)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Truncate(0); err != nil {
		_ = f.Close()
		return nil, err
	}
	if _, err := f.WriteAt(body, 0); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		_ = os.Remove(path)
	}, nil
}
