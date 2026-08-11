package main

import (
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/agenxy/lanes/internal/paths"
)

// writeServiceUnit writes an init-system unit so the daemon survives a closed
// terminal and a reboot.
//
// Every install path told people to run `lanesd &`, which ties the fleet's board
// to one shell. Closing that window, or rebooting, takes the board away from
// every agent still running, and nothing tells them why, because from their
// side coordination simply stops answering. A coordination service that a
// laptop lid can end is not a service.
//
// It WRITES the unit and prints the command to load it, rather than loading it
// itself. Registering a background job that starts at login is a change to the
// machine, not to Lanes, and the operator should be the one who makes it: the
// same reason nothing here ever drives a harness.
func writeServiceUnit() error {
	daemon, err := daemonPath()
	if err != nil {
		return err
	}
	// ABSOLUTE. A unit does not inherit the invoking shell's working directory,
	// so `LANES_DIR=relative-data lanes configure --service` wrote
	// `-dir relative-data` and produced a service that starts against whatever
	// that resolves to under launchd or systemd: a different board, silently.
	dir, err := filepath.Abs(paths.DataDir())
	if err != nil {
		return fmt.Errorf("resolving the data directory to an absolute path: %w", err)
	}
	// Both unit formats are text with their own metacharacters, and a path is
	// data. Reject what cannot be represented rather than mangling it: a data
	// directory is an identity, and a service that starts on a REPLACED path
	// starts on the wrong state.
	for _, v := range []string{daemon, dir} {
		if err := usableInAUnitFile(v); err != nil {
			return err
		}
	}

	switch runtime.GOOS {
	case "darwin":
		return writeLaunchAgent(daemon, dir)
	case "linux":
		return writeSystemdUnit(daemon, dir)
	default:
		return fmt.Errorf("no service unit for %s: run `%s` under whatever supervises "+
			"long-lived processes on this system", runtime.GOOS, daemon)
	}
}

// daemonPath resolves lanesd beside this binary first, then PATH.
//
// Beside-first because that is how they are installed together, and a unit file
// records an absolute path that outlives the shell that wrote it: resolving to
// a different lanesd than the one the operator just installed produces a service
// that runs the wrong build indefinitely, with a version skew nothing reports.
func daemonPath() (string, error) {
	if self, err := os.Executable(); err == nil {
		beside := filepath.Join(filepath.Dir(self), "lanesd")
		if st, err := os.Stat(beside); err == nil && !st.IsDir() {
			return beside, nil
		}
	}
	found, err := exec.LookPath("lanesd")
	if err != nil {
		return "", fmt.Errorf("cannot find lanesd beside %s or on PATH: install it first "+
			"(`task install`, or the release archive)", "lanes")
	}
	return found, nil
}

// usableInAUnitFile rejects values no unit format can carry faithfully.
//
// XML 1.0 cannot represent most control characters at all. Go's escaper
// replaces them, so a directory containing 0x01 silently became a DIFFERENT
// directory in the plist, and the service would have started on other state.
// Silent replacement is never acceptable for something that identifies data.
func usableInAUnitFile(v string) error {
	if !utf8.ValidString(v) {
		return fmt.Errorf("path is not valid UTF-8 and cannot be written to a service "+
			"unit faithfully: %q", v)
	}
	for _, r := range v {
		// Tab, newline and carriage return are legal in XML but break systemd's
		// line-oriented format, and none of them belong in a path a service
		// starts from. The rest of C0, plus DEL, cannot be represented in XML 1.0.
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("path contains the control character %q, which cannot be "+
				"written to a service unit without changing it: %q", r, v)
		}
		if !xmlRepresentable(r) {
			return fmt.Errorf("path contains %U, which XML cannot represent and the "+
				"encoder replaces with U+FFFD, producing a service that starts on a "+
				"different directory: %q", r, v)
		}
	}
	return nil
}

// xmlRepresentable reports whether a rune survives XML 1.0 serialization
// unchanged. The ranges are the Char production from the XML specification.
//
// The C0 check above is not enough. U+FFFE and U+FFFF are ordinary runes in a Go
// string and a legal filename on macOS, and they sit just outside XML's range,
// so Go's encoder silently substitutes U+FFFD. A validator that accepted them
// wrote a plist naming a directory that was one byte-sequence different from the
// one asked for: the service starts, on the wrong state, saying nothing. This is
// the same class as the control-character case and was missed because C0 is
// where one expects the problem to be.
func xmlRepresentable(r rune) bool {
	switch {
	case r == 0x09, r == 0x0a, r == 0x0d:
		return true
	case r >= 0x20 && r <= 0xd7ff:
		return true
	case r >= 0xe000 && r <= 0xfffd:
		return true
	case r >= 0x10000 && r <= 0x10ffff:
		// Plane-local noncharacters (U+1FFFE, U+2FFFE, ...) are inside Char and
		// do round-trip, so they are not this function's business.
		return true
	}
	return false
}

// systemdArg quotes one ExecStart argument.
//
// Both values were concatenated raw, and systemd's command line is neither a
// shell nor a literal: it splits on whitespace, expands `%` specifiers, and
// treats a newline as the end of the directive. Legal paths therefore did three
// distinct things. "bin with space/lanes" became two arguments, "%n" expanded
// to the unit name, and a newline let the rest of the path inject
// `Environment=` into the unit. Double-quoting handles the first, doubling `%`
// the second, and usableInAUnitFile refuses the third.
//
// `$` is the fourth, and was missed on the first pass. systemd expands `$VAR`
// and `${VAR}` inside this command syntax even though it is not a shell, so
// `/opt/$LANES_BIN/lanesd` starts whatever the environment says, or nothing at
// all when the variable is unset. A literal dollar is `$$`.
func systemdArg(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	v = strings.ReplaceAll(v, "%", "%%")
	v = strings.ReplaceAll(v, "$", "$$")
	return `"` + v + `"`
}

// shellArg quotes a value for a command a human will paste into a shell.
//
// The refusal below prints `launchctl unload -w <path>` and `rm <path>`. With a
// space in the path those are not runnable as printed, and with a backtick or a
// `$(...)` in one they are worse than not runnable. Single quotes suppress every
// expansion a shell performs; the only character that needs care inside them is
// the single quote itself, which cannot be escaped and must be closed, escaped
// and reopened.
func shellArg(v string) string {
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}

// xmlText escapes a value for a plist <string>. Paths were concatenated into
// the XML directly, so a perfectly ordinary directory. "~/Fleet & Review",
// produced a plist that launchd refuses to parse ("unknown ampersand-escape
// sequence"). The service then simply never starts, and the failure surfaces
// far from its cause.
func xmlText(v string) string {
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(v)); err != nil {
		return v // EscapeText to a strings.Builder cannot fail
	}
	return b.String()
}

// legacyLabels are identifiers Lanes used before settling on org.agenxy.lanes.
//
// A user who followed the v0.0.1 instructions has a LOADED job under the old
// label. Writing the new one and saying nothing gives them two daemons fighting
// for the same data directory: the flock refuses the second, so the visible
// result is a service that mysteriously will not start.
var legacyLabels = []string{"dev.agenxy.lanes", "com.agenxy.lanes"}

// refuseIfLegacyUnitExists stops rather than creating a second job.
//
// Not migrated automatically: unloading somebody's running service is an action
// on their machine, and the same reasoning that has this command print
// `launchctl load` instead of running it applies to unloading. Refusing with the
// exact commands is the honest middle.
func refuseIfLegacyUnitExists(agentsDir string) error {
	for _, label := range legacyLabels {
		old := filepath.Join(agentsDir, label+".plist")
		// #nosec G703 -- agentsDir is this process's own HOME and label is a
		// fixed constant from legacyLabels; no caller-supplied path reaches here.
		if _, err := os.Stat(old); err != nil {
			continue
		}
		return fmt.Errorf("a unit from an earlier version is still installed: %s\n"+
			"  Lanes now uses org.agenxy.lanes. Writing the new one as well would leave two\n"+
			"  jobs pointing at one data directory, and the directory lock refuses the second,\n"+
			"  which looks like a service that will not start.\n\n"+
			"  Remove the old one first:\n"+
			"    launchctl unload -w %s\n"+
			"    rm %s\n"+
			"  then run this again", old, shellArg(old), shellArg(old))
	}
	return nil
}

func writeLaunchAgent(daemon, dir string) error {
	agents := filepath.Join(os.Getenv("HOME"), "Library", "LaunchAgents")
	if err := refuseIfLegacyUnitExists(agents); err != nil {
		return err
	}
	target := filepath.Join(agents, "org.agenxy.lanes.plist")
	logPath := filepath.Join(dir, "lanesd.log")
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>org.agenxy.lanes</string>
  <key>ProgramArguments</key>
  <array>
    <string>` + xmlText(daemon) + `</string>
    <string>-dir</string>
    <string>` + xmlText(dir) + `</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key>
  <dict><key>SuccessfulExit</key><false/></dict>
  <key>StandardOutPath</key><string>` + xmlText(logPath) + `</string>
  <key>StandardErrorPath</key><string>` + xmlText(logPath) + `</string>
  <key>ProcessType</key><string>Background</string>
</dict>
</plist>
`
	if err := writeUnit(target, plist); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n\nLoad it:\n  launchctl load -w %s\n\n"+
		"KeepAlive restarts it on a crash but not after a clean `lanes stop`, so\n"+
		"stopping it stays stopped until you start it again.\nLogs: %s\n",
		target, target, logPath)
	return nil
}

func writeSystemdUnit(daemon, dir string) error {
	target := filepath.Join(configHome(), "systemd", "user", "lanes.service")
	unit := `[Unit]
Description=Lanes: coordination board for AI agents
Documentation=https://github.com/agenxy/lanes
After=network.target

[Service]
Type=simple
ExecStart=` + systemdArg(daemon) + ` -dir ` + systemdArg(dir) + `
Restart=on-failure
RestartSec=2

[Install]
WantedBy=default.target
`
	if err := writeUnit(target, unit); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n\nEnable and start it:\n  systemctl --user enable --now lanes\n\n"+
		"Logs: journalctl --user -u lanes -f\n", target)
	return nil
}

// configHome honours XDG_CONFIG_HOME, because a systemd user unit written to
// the wrong place is silently never loaded.
func configHome() string {
	if x := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); x != "" {
		return x
	}
	return filepath.Join(os.Getenv("HOME"), ".config")
}

// writeUnit refuses to clobber. An operator who hand-tuned a unit: a different
// port, an environment variable, a resource limit: should not lose it to a
// command that reads as idempotent.
func writeUnit(target, body string) error {
	// target is built in this file from the process's own HOME (or
	// XDG_CONFIG_HOME) plus a fixed filename. No caller-supplied path reaches
	// it, so the taint analysis has nothing to protect against here.
	parent := filepath.Dir(target) // #nosec G703
	// 0o750: the init system reads these as the same user, so nothing wider is
	// needed. ~/Library/LaunchAgents and ~/.config are the user's own.
	if err := os.MkdirAll(parent, 0o750); err != nil { // #nosec G703
		return err
	}
	if _, err := os.Stat(target); err == nil { // #nosec G703
		return fmt.Errorf("%s already exists: delete it first if you want a fresh one, "+
			"or edit it in place; refusing to overwrite a unit you may have tuned", target)
	}
	// #nosec G306,G703 -- a unit file the init system must be able to read, at a
	// path built from HOME and a fixed filename.
	return os.WriteFile(target, []byte(body), 0o644)
}
