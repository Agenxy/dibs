package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Preflight has to ask the question the real write will ask, not a cheaper one.
//
// The rewrite is refused for two independent reasons. One is the file mode,
// which unitIsWritable covers. The other is that a unit under one of the legacy
// labels is still installed: writing the current one beside it would leave two
// jobs on one data directory, so writeServiceUnit refuses outright, and
// `replaceUnits` deliberately does not waive that.
//
// Only the first was checked before the daemon was stopped. So an operator
// still on a legacy-labelled unit, which is precisely the installation this
// command exists to migrate, got: preflight passes, daemon stopped, rewrite
// refused for a reason that was knowable the whole time, and recovery restarts
// through that same legacy unit, whose ExecStart still pins the OLD binary,
// while printing "This is the NEW build, not a rollback". An upgrade that did
// not upgrade and reported that it had.
//
// The assertion is that NOTHING WAS STOPPED, which is the promise preflight
// makes, so this is written against preflight's answer rather than against any
// particular sentence in it.
func TestPreflightRefusesALegacyUnitBeforeStoppingAnything(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	// The legacy unit, in the place the refusal looks for it, for this platform.
	var unitDir, legacy string
	switch runtime.GOOS {
	case "darwin":
		unitDir = filepath.Join(home, "Library", "LaunchAgents")
		legacy = filepath.Join(unitDir, legacyLabels[0]+".plist")
	case "linux":
		unitDir = filepath.Join(home, ".config", "systemd", "user")
		legacy = filepath.Join(unitDir, legacySystemdUnits[0])
	default:
		t.Skipf("no service manager on %s", runtime.GOOS)
	}
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatal("setup:", err)
	}
	// Pinning an old binary, which is what makes the plan want a rewrite.
	if err := os.WriteFile(legacy, []byte("ExecStart=/old/bin/dibd\n"), 0o644); err != nil {
		t.Fatal("setup:", err)
	}

	p := &plan{
		unit:      legacy,
		pinned:    "/old/bin/dibd",
		installed: "/new/bin/dibd",
		unitWrong: true,
	}

	// The setup has to be true or the assertion means nothing: the file must be
	// writable, so that a refusal below is the LEGACY refusal and not the
	// permissions one that was already covered.
	if err := unitIsWritable(p.unit); err != nil {
		t.Fatalf("setup: the unit is not writable (%v), so this would be testing the "+
			"check that already existed", err)
	}

	err := p.preflight()
	if err == nil {
		t.Fatalf("preflight passed with %s installed. The rewrite after the stop is "+
			"refused by that file, so the daemon gets stopped for an upgrade that "+
			"cannot complete, and recovery restarts the OLD binary through this very "+
			"unit while reporting the new one", legacy)
	}
	// It must also be the refusal we mean, and it must say nothing was stopped,
	// because that is the guarantee an operator acts on.
	if !strings.Contains(err.Error(), "earlier version") {
		t.Errorf("preflight refused for some other reason than the legacy unit, so "+
			"this test is not watching what it claims: %v", err)
	}
	if !strings.Contains(err.Error(), "nothing has been stopped") {
		t.Errorf("the refusal does not tell the operator the board is still up, which "+
			"is the one thing they need to know before deciding what to do: %v", err)
	}
}

// And the converse, or the check above could simply refuse everything: an
// ordinary upgrade, whose only installed unit is the CURRENT one, still gets
// through. That is the common path, and a preflight that blocked it would stop
// every upgrade on every machine.
func TestPreflightStillPassesAnOrdinaryUnitRewrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	var unitDir, current string
	switch runtime.GOOS {
	case "darwin":
		unitDir = filepath.Join(home, "Library", "LaunchAgents")
		current = filepath.Join(unitDir, "org.agenxy.dibs.plist")
	case "linux":
		unitDir = filepath.Join(home, ".config", "systemd", "user")
		current = filepath.Join(unitDir, "dibs.service")
	default:
		t.Skipf("no service manager on %s", runtime.GOOS)
	}
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatal("setup:", err)
	}
	if err := os.WriteFile(current, []byte("ExecStart=/old/bin/dibd\n"), 0o644); err != nil {
		t.Fatal("setup:", err)
	}

	p := &plan{
		unit:      current,
		pinned:    "/old/bin/dibd",
		installed: "/new/bin/dibd",
		unitWrong: true,
	}
	if err := p.preflight(); err != nil {
		t.Errorf("preflight refused an ordinary unit rewrite, which is what upgrade "+
			"does on every machine that has ever installed the service: %v", err)
	}
}
