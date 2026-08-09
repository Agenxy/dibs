package liveness

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// One [supervise] table configures every surface.
//
// It existed for the daemon and `lanes probe` ignored it, so the same five
// judgements lived in two forms — a file the sweep honoured, and flags on the
// command a person actually runs. Demonstrating stall detection meant spelling
// the configuration out loud on every invocation.
func TestOneTableConfiguresEverySurface(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lanes.toml"), []byte(
		"[supervise]\nmin_age = \"1s\"\nmin_duty = 0.05\nevery = \"3s\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := LoadSettings(dir).Apply(DefaultConfig())

	if got.MinAge != time.Second || got.MinDuty != 0.05 {
		t.Errorf("the file was not applied: MinAge=%v MinDuty=%v", got.MinAge, got.MinDuty)
	}
	// The fields it did NOT mention must keep the measured defaults, not become
	// zero. A zero threshold is not "no threshold" — it is "everything is
	// stuck" or "nothing is" depending on the comparison, and assigning a
	// Config wholesale from partial input has silently disabled a check here
	// before.
	def := DefaultConfig()
	if got.Quiet != def.Quiet || got.Frozen != def.Frozen {
		t.Errorf("unmentioned fields were zeroed: Quiet=%v Frozen=%v", got.Quiet, got.Frozen)
	}
	if c := LoadSettings(dir).Cadence(); c != 3*time.Second {
		t.Errorf("cadence = %v, want 3s", c)
	}
}

// A missing or broken file must leave the defaults standing. Most installs
// never write one, and a supervision layer that refused to start because an
// optional file was absent would be worse than one with no configuration.
func TestAnAbsentOrBrokenFileLeavesTheDefaults(t *testing.T) {
	def := DefaultConfig()
	for name, dir := range map[string]string{
		"absent": filepath.Join(t.TempDir(), "nothing-here"),
		"broken": t.TempDir(),
	} {
		if name == "broken" {
			if err := os.WriteFile(filepath.Join(dir, "lanes.toml"), []byte("[supervise\nmin_age ="), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		got := LoadSettings(dir).Apply(def)
		if got != def {
			t.Errorf("%s file changed the config: %+v", name, got)
		}
		if c := LoadSettings(dir).Cadence(); c != SweepEvery {
			t.Errorf("%s file changed the cadence: %v", name, c)
		}
	}
}
