package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A unit file records an absolute path and then outlives the shell that wrote
// it. Writing the wrong one produces a service that runs a different build
// indefinitely, or silently overwrites a unit somebody tuned by hand.
func TestServiceUnitsAreWrittenSafely(t *testing.T) {
	t.Run("refuses to overwrite an existing unit", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "nested", "lanes.service")
		if err := writeUnit(target, "original"); err != nil {
			t.Fatal(err)
		}
		if err := writeUnit(target, "replacement"); err == nil {
			t.Fatal("overwrote an existing unit: an operator's tuning is gone and the " +
				"command reads as idempotent")
		}
		body, err := os.ReadFile(target) // #nosec G304 -- test temp dir
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != "original" {
			t.Fatalf("the existing unit was modified: %q", body)
		}
	})

	t.Run("creates the parent directory", func(t *testing.T) {
		// ~/.config/systemd/user and ~/Library/LaunchAgents do not always exist,
		// and a unit written nowhere is a service that never starts.
		target := filepath.Join(t.TempDir(), "a", "b", "c", "lanes.service")
		if err := writeUnit(target, "unit"); err != nil {
			t.Fatalf("could not create the parent path: %v", err)
		}
		if _, err := os.Stat(target); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("configHome honours XDG_CONFIG_HOME", func(t *testing.T) {
		// A systemd user unit written to the wrong root is never loaded, and
		// nothing reports that: the service simply does not exist.
		t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-probe")
		if got := configHome(); got != "/tmp/xdg-probe" {
			t.Errorf("configHome() = %q, want the XDG value", got)
		}
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", "/tmp/home-probe")
		if got := configHome(); !strings.HasSuffix(got, "/.config") {
			t.Errorf("configHome() = %q, want ~/.config when XDG is unset", got)
		}
	})
}

// Paths go into the plist as XML text. A perfectly ordinary directory name
// containing "&" produced a plist launchd refuses to parse, so the service
// silently never started and the failure surfaced far from its cause.
func TestTheWrittenPlistParsesWithAwkwardPaths(t *testing.T) {
	// The unit test below covers xmlText. This covers the thing that actually
	// ships: reverting writeLaunchAgent to concatenate a raw path left xmlText
	// perfectly correct and never called, which is this codebase's most repeated
	// defect: a helper that is right and unwired.
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "Fleet & Review")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := writeLaunchAgent(filepath.Join(home, "bin", "lanesd"), dir); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(home, "Library", "LaunchAgents", "org.agenxy.lanes.plist"))
	if err != nil {
		t.Fatal(err)
	}
	// launchd will not load a plist it cannot parse, and says so nowhere the
	// operator is looking: the service simply never starts.
	dec := xml.NewDecoder(bytes.NewReader(body))
	for {
		_, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("the generated plist is not well-formed XML: %v\n%s", err, body)
		}
	}
	if !bytes.Contains(body, []byte("Fleet &amp; Review")) {
		t.Errorf("the ampersand was not escaped in the written unit:\n%s", body)
	}
}

func TestServiceUnitEscapesPathsForXML(t *testing.T) {
	for _, in := range []string{
		`/Users/x/Fleet & Review`,
		`/Users/x/<odd>`,
		`/Users/x/quote"dir`,
	} {
		got := xmlText(in)
		if strings.ContainsAny(strings.NewReplacer("&amp;", "", "&lt;", "", "&gt;", "",
			"&#34;", "", "&#39;", "").Replace(got), `&<>`) {
			t.Errorf("xmlText(%q) = %q: still contains raw markup", in, got)
		}
	}
	if got := xmlText("/plain/path"); got != "/plain/path" {
		t.Errorf("xmlText mangled an ordinary path: %q", got)
	}
}

// systemd's ExecStart is neither a shell nor a literal: it splits on
// whitespace, expands `%` specifiers, and ends the directive at a newline. Both
// values were concatenated raw, so legal paths did three different damaging
// things. This asserts the WRITTEN unit, because escaping the helper and
// forgetting the call site is how the XML half shipped broken.
func TestSystemdUnitCannotBeInjectedThroughAPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))

	daemon := filepath.Join(home, "bin with space", "lanesd")
	dir := filepath.Join(home, "Fleet %n and \"quoted\"")
	if err := writeSystemdUnit(daemon, dir); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(home, "cfg", "systemd", "user", "lanes.service"))
	if err != nil {
		t.Fatal(err)
	}
	var exec string
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "ExecStart=") {
			exec = line
		}
	}
	if exec == "" {
		t.Fatalf("no ExecStart in the written unit:\n%s", body)
	}
	// A bare % is a specifier. Literal percent must be doubled, or systemd
	// expands %n to the unit name and starts on a different directory.
	if strings.Contains(strings.ReplaceAll(exec, "%%", ""), "%") {
		t.Errorf("ExecStart carries an unescaped %% specifier: %s", exec)
	}
	// Each path must survive as ONE argument despite its spaces.
	if !strings.Contains(exec, `"`+daemon+`"`) {
		t.Errorf("the daemon path is not quoted as a single argument: %s", exec)
	}
	// Nothing may introduce another directive.
	for _, d := range []string{"\nEnvironment=", "\nExecStartPost=", "\n[Service]"} {
		if strings.Contains(string(body), d[1:]+"SOL") {
			t.Errorf("a path injected %q into the unit:\n%s", d, body)
		}
	}
}

// A unit does not inherit the invoking shell's working directory, so a relative
// LANES_DIR produced a service that starts against whatever it resolves to
// under launchd: a different board, silently.
func TestServiceUnitsRecordAnAbsoluteDataDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LANES_DIR", "relative-data")
	t.Chdir(home)
	if err := os.MkdirAll(filepath.Join(home, "relative-data"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := writeServiceUnit(); err != nil {
		// No lanesd on PATH in a test environment is fine; the point is the path.
		if strings.Contains(err.Error(), "cannot find lanesd") {
			t.Skip("no lanesd available to reference")
		}
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(home, "Library", "LaunchAgents", "org.agenxy.lanes.plist"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte(">relative-data<")) {
		t.Errorf("the unit records a relative data directory:\n%s", body)
	}
}

// Upgrading from a version that used a different label must not leave two jobs
// pointing at one data directory: the flock refuses the second, so the visible
// symptom is a service that will not start for no stated reason.
func TestUpgradeRefusesToCreateASecondJob(t *testing.T) {
	agents := t.TempDir()
	for _, label := range legacyLabels {
		old := filepath.Join(agents, label+".plist")
		if err := os.WriteFile(old, []byte("<plist/>"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := refuseIfLegacyUnitExists(agents)
		if err == nil {
			t.Fatalf("%s was ignored; a second job would have been created", label)
		}
		// The refusal has to carry the way out, not just the objection.
		for _, want := range []string{"launchctl unload", "rm ", old} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal does not tell the operator how to proceed (%q):\n%v", want, err)
			}
		}
		if err := os.Remove(old); err != nil {
			t.Fatal(err)
		}
	}
	if err := refuseIfLegacyUnitExists(agents); err != nil {
		t.Errorf("a clean machine was refused: %v", err)
	}
}
