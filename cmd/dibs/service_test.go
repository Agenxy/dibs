package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A unit file records an absolute path and then outlives the shell that wrote
// it. Writing the wrong one produces a service that runs a different build
// indefinitely, or silently overwrites a unit somebody tuned by hand.
func TestServiceUnitsAreWrittenSafely(t *testing.T) {
	t.Run("refuses to overwrite an existing unit", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "nested", "agents.service")
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
		target := filepath.Join(t.TempDir(), "a", "b", "c", "agents.service")
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
	if err := writeLaunchAgent(filepath.Join(home, "bin", "dibd"), dir); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(home, "Library", "LaunchAgents", "org.agenxy.dibs.plist"))
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

	daemon := filepath.Join(home, "bin with space", "dibd")
	dir := filepath.Join(home, "Fleet %n and \"quoted\"")
	if err := writeSystemdUnit(daemon, dir); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(home, "cfg", "systemd", "user", "dibs.service"))
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
// DIBS_DIR produced a service that starts against whatever it resolves to
// under launchd: a different board, silently.
func TestServiceUnitsRecordAnAbsoluteDataDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DIBS_DIR", "relative-data")
	t.Chdir(home)
	if err := os.MkdirAll(filepath.Join(home, "relative-data"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := writeServiceUnit(); err != nil {
		// No dibd on PATH in a test environment is fine; the point is the path.
		if strings.Contains(err.Error(), "cannot find dibd") {
			t.Skip("no dibd available to reference")
		}
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(home, "Library", "LaunchAgents", "org.agenxy.dibs.plist"))
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

// systemd expands `$VAR` and `${VAR}` inside ExecStart even though its command
// line is not a shell. A daemon at /opt/$DIBS_BIN/dibd therefore started
// whatever the environment said, or failed to start at all when the variable was
// unset. Reported after the first round of escaping shipped: backslash, quote
// and percent were handled and dollar was not, which is the shape of every miss
// in this file.
func TestSystemdUnitDoesNotExpandVariablesFromAPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))

	daemon := filepath.Join(home, "opt", "$DIBS_BIN", "dibd")
	dir := filepath.Join(home, "data", "${USER}", "fleet")
	if err := writeSystemdUnit(daemon, dir); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(home, "cfg", "systemd", "user", "dibs.service"))
	if err != nil {
		t.Fatal(err)
	}
	var exec string
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "ExecStart=") {
			exec = line
		}
	}
	// Every literal dollar must be doubled. Stripping the doubled pairs must
	// leave none behind, exactly as the percent check does.
	if strings.Contains(strings.ReplaceAll(exec, "$$", ""), "$") {
		t.Errorf("ExecStart leaves a dollar for systemd to expand: %s", exec)
	}
	for _, want := range []string{"$$DIBS_BIN", "$${USER}"} {
		if !strings.Contains(exec, want) {
			t.Errorf("ExecStart does not carry %q literally: %s", want, exec)
		}
	}
}

// U+FFFE and U+FFFF are ordinary runes in Go and legal in a macOS filename, and
// they sit just outside XML's Char production, so the encoder replaces them with
// U+FFFD without error. The unit then names a directory one byte-sequence away
// from the one asked for and the service starts on the wrong state, silently.
// The C0 check did not catch these because C0 is where one expects the problem.
func TestPathsXMLCannotRepresentAreRefusedRatherThanChanged(t *testing.T) {
	for _, r := range []rune{'￾', '￿'} {
		bad := "/data/" + string(r) + "/fleet"
		if err := usableInAUnitFile(bad); err == nil {
			t.Errorf("%U was accepted; the written unit would point somewhere else", r)
		}
	}
	// The replacement character itself is representable: a path that genuinely
	// contains one round-trips unchanged, so refusing it would be wrong.
	if err := usableInAUnitFile("/data/�/fleet"); err != nil {
		t.Errorf("a path containing U+FFFD was refused, but it survives XML intact: %v", err)
	}
	// Supplementary-plane characters are ordinary and must not be caught.
	if err := usableInAUnitFile("/data/\U0001F600/fleet"); err != nil {
		t.Errorf("an emoji in a path was refused: %v", err)
	}
}

// The refusal promises exact corrective commands. With a space in the path the
// printed `rm` was two arguments, and with a backtick or `$(...)` it would have
// been worse than merely broken.
func TestTheCorrectiveCommandsAreSafeToPaste(t *testing.T) {
	agents := filepath.Join(t.TempDir(), "Fleet 'A' $(whoami)")
	if err := os.MkdirAll(agents, 0o750); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(agents, legacyLabels[0]+".plist")
	if err := os.WriteFile(old, []byte("<plist/>"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := refuseIfLegacyUnitExists(agents)
	if err == nil {
		t.Fatal("the legacy unit was ignored")
	}
	if !strings.Contains(err.Error(), "rm "+shellArg(old)) {
		t.Errorf("the rm command is not quoted for a shell:\n%v", err)
	}
	if strings.Contains(err.Error(), "rm "+old) {
		t.Errorf("the rm command was printed raw and would not run as shown:\n%v", err)
	}
}

func TestShellArgSurvivesQuotesAndExpansions(t *testing.T) {
	for in, want := range map[string]string{
		`/plain/path`:  `'/plain/path'`,
		`/a b/c`:       `'/a b/c'`,
		`/it's/here`:   `'/it'\''s/here'`,
		"/`whoami`/x":  "'/`whoami`/x'",
		`/$(whoami)/x`: `'/$(whoami)/x'`,
		`/a"b/c`:       `'/a"b/c'`,
	} {
		if got := shellArg(in); got != want {
			t.Errorf("shellArg(%q) = %q, want %q", in, got, want)
		}
	}
}

// The unit a Linux user is told to start must be named for the product.
//
// The rename swept the systemd unit along with everything else, so the file was
// `agents.service` and the printed instruction was `systemctl --user enable
// --now agents`, for a product called dibs. Same class as the four naming bugs
// that reached releases: correct code, wrong noun, invisible to a suite whose
// fixtures the same sweep rewrote. Asserted against a literal, so a future
// rename has to come and change it deliberately.
func TestSystemdUnitIsNamedForTheProduct(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	if err := writeSystemdUnit(filepath.Join(home, "bin", "dibd"), filepath.Join(home, ".dibs")); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".config", "systemd", "user", "dibs.service")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("no unit at %s: a Linux user following the printed command starts nothing", want)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "systemd", "user", "agents.service")); err == nil {
		t.Error("still writing agents.service")
	}
}

// A second unit beside an old one is two daemons contending for one directory
// lock, which presents as a service that will not start. launchd has refused
// this since the com.agents.dibd incident; systemd never did.
func TestSystemdRefusesToCreateASecondUnit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	unitDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, legacy := range legacySystemdUnits {
		if err := os.WriteFile(filepath.Join(unitDir, legacy), []byte("[Unit]\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := writeSystemdUnit(filepath.Join(home, "bin", "dibd"), filepath.Join(home, ".dibs"))
		if err == nil {
			t.Errorf("wrote a second unit beside %s; both would point at one data directory", legacy)
		}
		if err != nil && !strings.Contains(err.Error(), legacy) {
			t.Errorf("the refusal does not name %s, so the operator cannot act on it: %v", legacy, err)
		}
		_ = os.Remove(filepath.Join(unitDir, legacy))
	}
}

// Advice to move the data directory is wrong if a unit pins the old path.
func TestUnitPinningFindsTheServiceHoldingAnOldPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	old := filepath.Join(home, ".agents")

	if got := unitPinning(old); got != "" {
		t.Errorf("reported a unit (%s) when none is installed", got)
	}

	agents := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(agents, "org.agenxy.dibs.plist")
	if err := os.WriteFile(plist, []byte("<string>"+old+"</string>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := unitPinning(old); got != plist {
		t.Errorf("unitPinning(%q) = %q, want %q: without this the hint tells the user to "+
			"move a directory the service will still be looking for", old, got, plist)
	}
}

// A unit pins an absolute path, so doctor has to be able to read it back out.
//
// Install the daemon from a Go workspace once and from `task install` later,
// and the service goes on starting the first one forever: the daemon answers,
// every other check passes against it, and every fix shipped since that build
// is simply not running. Nothing reported it, because nothing compared the two.
func TestTheUnitsPinnedDaemonIsReadable(t *testing.T) {
	for _, tc := range []struct {
		name, file, body, want string
	}{
		{
			name: "launchd plist", file: "Library/LaunchAgents/org.agenxy.dibs.plist",
			body: `<plist version="1.0"><dict>
  <key>ProgramArguments</key>
  <array>
    <string>/Users/someone/go/bin/dibd</string>
    <string>-dir</string>
    <string>/Users/someone/.dibs</string>
  </array>
</dict></plist>`,
			want: "/Users/someone/go/bin/dibd",
		},
		{
			name: "systemd unit", file: ".config/systemd/user/dibs.service",
			body: "[Service]\nExecStart=/home/someone/.local/bin/dibd -dir /home/someone/.dibs\n",
			want: "/home/someone/.local/bin/dibd",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			path := filepath.Join(home, tc.file)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}

			unit, daemon := unitDaemon()
			if daemon != tc.want {
				t.Errorf("pinned daemon = %q, want %q: doctor cannot compare what it cannot read", daemon, tc.want)
			}
			if unit != path {
				t.Errorf("unit = %q, want %q", unit, path)
			}
		})
	}
}

// No unit is not a fault: `configure --service` is optional.
func TestNoUnitReportsNothingToCompare(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	if unit, daemon := unitDaemon(); unit != "" || daemon != "" {
		t.Errorf("invented a unit: %q %q", unit, daemon)
	}
}

// Two spellings of one file are not a mismatch. A symlinked install would
// otherwise be reported as a wrong daemon on every doctor run, and a check that
// cries wolf is a check people stop reading.
func TestSameBinarySeesThroughASymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "dibd")
	if err := os.WriteFile(real, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "dibd-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	if !sameBinary(real, link) {
		t.Error("a symlink to the same file was reported as a different daemon")
	}
	if sameBinary(real, filepath.Join(dir, "nope")) {
		t.Error("a path that does not exist matched a real one")
	}
}

// Writing a service unit creates the directory the unit points its log at.
//
// THE SILENT INSTALL BLOCKER. The launchd unit names <datadir>/dibd.log for
// both StandardOutPath and StandardErrorPath, and launchd creates the log FILE
// but not its parent DIRECTORY. On a first install the directory does not exist
// yet, because the daemon makes it on its own first run, so the spawn fails
// before the binary runs and `launchctl list` shows a dash for the pid with
// last exit status ZERO: "ran and finished cleanly", which sends the operator
// looking at RunAtLoad instead of at whether anything was ever spawned. The one
// artefact that would explain it is the log, which is the artefact that cannot
// be created.
//
// Reported by an operator bringing up a second machine, with the diagnosis.
func TestWritingAServiceUnitCreatesTheDataDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh-install")
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("HOME", t.TempDir())

	// A dibd for daemonPath to find, because the machine running the tests may
	// not have one installed.
	//
	// This passed locally and failed on every runner: the mirror image of the
	// hostname test that passed on every runner and failed locally, and the
	// same mistake. A test that depends on what happens to be installed is
	// testing the machine.
	//
	// An empty file is enough and nothing executes it: daemonPath stats the
	// binary beside this one and otherwise LookPaths it, to record an absolute
	// path in the unit. It never runs it.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "dibd"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("setup: %s already exists, so this proves nothing", dir)
	}

	if err := writeServiceUnit(); err != nil {
		// A machine with no launchd/systemd is not what this is about.
		if strings.Contains(err.Error(), "no service unit for") {
			t.Skip(err)
		}
		t.Fatalf("writeServiceUnit: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("the unit was written and its log directory was not created, so the "+
			"service cannot start and says so with exit status 0: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", dir)
	}
}

// A refusal must change nothing on disk.
//
// The data directory was created before the conflict checks that then refuse,
// so `dibs configure --service` could report "refusing to overwrite a unit you
// may have tuned" having already made a board directory on the way to saying
// it. Where the loaded unit's directory had been moved or damaged, that
// recreated an empty board at the old path for the existing job to start
// against. Raised by the pre-release review.
func TestARefusedServiceInstallCreatesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	// A unit is already there, so the install must refuse.
	var unit string
	switch runtime.GOOS {
	case "darwin":
		unit = filepath.Join(home, "Library", "LaunchAgents", "org.agenxy.dibs.plist")
	case "linux":
		unit = filepath.Join(home, ".config", "systemd", "user", "dibs.service")
	default:
		t.Skip("no service unit on this platform")
	}
	if err := os.MkdirAll(filepath.Dir(unit), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unit, []byte("existing, possibly tuned"), 0o600); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(t.TempDir(), "board")
	t.Setenv("DIBS_DIR", dir)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "dibd"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	if err := writeServiceUnit(); err == nil {
		t.Fatal("an existing unit was overwritten")
	}
	if _, err := os.Stat(dir); err == nil {
		t.Errorf("the install refused and still created %s: a refusal that changes "+
			"the board on disk is not a refusal", dir)
	}
}
