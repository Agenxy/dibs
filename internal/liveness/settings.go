package liveness

import (
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

// Settings is the `[supervise]` table of `<dir>/lanes.toml`.
//
// It lives here, beside the thing it configures, because BOTH the daemon and
// `lanes probe` need it and they are different binaries. Keeping it in the
// daemon meant the CLI could only be tuned by flags, so the same five
// judgements existed in two forms, and an operator who set them in the file
// found the command still using the defaults. Somebody demonstrating stall
// detection then has to type
//
//	lanes probe --pid N --min-age 1s --min-duty 0.05
//
// which is a configuration file spelled out loud on every invocation.
//
// Zero means "not set" for every field, and an unset field keeps the measured
// default rather than becoming zero. That distinction is the whole reason this
// type exists separately from Config: a zero threshold does not mean "no
// threshold", it means "everything is stuck" or "nothing is" depending on the
// comparison, and building a Config field-by-field from partial input has
// already silently disabled a check once in this codebase.
type Settings struct {
	// Every is how often the machine is scanned. Scanning faster does not find
	// a stall sooner (nothing becomes stuck in less than Frozen) it only
	// costs more.
	Every time.Duration `toml:"every"`
	// Quiet is how long output may pause before an agent stops counting as
	// working. Measured at 15-second intervals, a healthy agent between turns
	// went a whole window with no output at all, so the default is far above.
	Quiet time.Duration `toml:"quiet"`
	// Frozen is how long BOTH output and CPU may stay flat, in AWAKE time,
	// before the verdict is stuck. The number that matters most.
	Frozen time.Duration `toml:"frozen"`
	// MinAge is how old a process must be before a whole-life idleness verdict
	// is allowed. Below it, there is no life to have been idle for.
	MinAge time.Duration `toml:"min_age"`
	// MinDuty is the fraction of its life a process must have spent on the CPU
	// to escape that verdict. Every program burns CPU starting up, so over a
	// short life that fixed cost is a large share of it, which is why tuning
	// MinAge down without raising this acquits everything.
	MinDuty float64 `toml:"min_duty"`
	// Off stops the DAEMON volunteering stall reports. `lanes probe` still
	// answers on demand; this is not a way to turn detection off, only a way to
	// stop being told.
	Off bool `toml:"off"`
}

// SweepEvery is how often the daemon scans when nothing says otherwise.
const SweepEvery = 20 * time.Second

// LoadSettings reads the `[supervise]` table from a data directory.
//
// A missing or unreadable file is not an error: most installs never write one,
// and a supervision layer that refused to start because an optional file was
// absent would be worse than one with no configuration at all.
func LoadSettings(dir string) Settings {
	var doc struct {
		Supervise Settings `toml:"supervise"`
	}
	b, err := os.ReadFile(filepath.Join(dir, "lanes.toml")) // #nosec G304 -- the operator's own data dir
	if err != nil {
		return Settings{}
	}
	if _, err := toml.Decode(string(b), &doc); err != nil {
		return Settings{}
	}
	return doc.Supervise
}

// Apply overlays whatever the settings actually specify onto the defaults.
//
// Field by field, and only where non-zero, so a file that sets one number does
// not silently zero the other four.
func (s Settings) Apply(base Config) Config {
	if s.Quiet > 0 {
		base.Quiet = s.Quiet
	}
	if s.Frozen > 0 {
		base.Frozen = s.Frozen
	}
	if s.MinAge > 0 {
		base.MinAge = s.MinAge
	}
	if s.MinDuty > 0 {
		base.MinDuty = s.MinDuty
	}
	return base
}

// Cadence is how often to sweep, defaulted.
func (s Settings) Cadence() time.Duration {
	if s.Every > 0 {
		return s.Every
	}
	return SweepEvery
}
