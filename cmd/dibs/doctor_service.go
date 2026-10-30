// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/agenxy/dibs/internal/paths"
)

func (d *diagnosis) serviceDirectoryNote(selected paths.DirectorySelection) {
	dir := selected.Dir
	if runtime.GOOS == "darwin" {
		serviceSelection := selected
		if pinned, found, err := paths.PinnedService(); err == nil &&
			found && filepath.Clean(pinned.Dir) == filepath.Clean(dir) {
			serviceSelection = pinned
		}
		servicePath, source := serviceSelection.EffectiveServicePath()
		d.note("service PATH (" + source + "): " + servicePath)
	}
}

func checkServiceBinary(ok reportFn, warn, bad fixFn, serving bool) {
	checkAppService(ok, warn, bad, serving)
	unit, pinned := unitDaemon()
	if unit == "" || pinned == "" {
		reportMissingService("Dibs is installed but no service is registered", serving, warn, bad)
		return
	}
	want, err := daemonPath()
	if err != nil {
		return // already reported by the checks above
	}
	app, _ := pairedServiceApp(want)
	if sameBinary(pinned, want) || (app != "" && sameBinary(pinned, filepath.Join(app, "Contents", "MacOS", "dibd"))) {
		ok("the service starts the dibd you have installed")
		return
	}
	detail := "it pins " + pinned + ", but the dibd you have installed is " + want
	if _, err := os.Stat(pinned); err != nil {
		detail = fmt.Sprintf("cannot inspect %s: %v; the installed dibd is %s", pinned, err, want)
		if os.IsNotExist(err) {
			detail = "it pins " + pinned + ", which does not exist; the dibd you have installed is " + want
		}
		if runtime.GOOS == "linux" && os.IsNotExist(err) {
			bad("the service executable does not exist", detail+". "+systemdDoctorFix(unit, want))
			return
		}
	}
	if runtime.GOOS == "linux" {
		warn("the service would start a different daemon than the one installed", detail+". "+systemdDoctorFix(unit, want))
		return
	}
	warn("the service would start a different daemon than the one installed",
		detail+". Every fix since that build is not running when the service starts it. "+
			"`dibs upgrade` rewrites the unit and restarts onto the installed daemon, "+
			"after checking that it can rebuild this board")
}

// sameBinary compares two paths by inode where it can, so a symlink or a
// ./bin/../bin spelling is not reported as a mismatch.
func sameBinary(a, b string) bool {
	sa, erra := os.Stat(a)
	sb, errb := os.Stat(b)
	if erra == nil && errb == nil {
		return os.SameFile(sa, sb)
	}
	ea, _ := filepath.EvalSymlinks(a)
	eb, _ := filepath.EvalSymlinks(b)
	return ea != "" && ea == eb
}

func systemdDoctorFix(unit, want string) string {
	selected, err := paths.SelectDir(false)
	body, readErr := os.ReadFile(unit) // #nosec G304 G703 -- this user's configured service unit
	if err == nil && readErr == nil && refreshableSystemdUnit(unit, string(body), selected.Dir, want) {
		return "Run `dibs upgrade` to refresh this Dibs-written unit and restart after verifying " +
			"the installed daemon can rebuild the board"
	}
	return "This unit has local changes or names another board. Edit ExecStart to the stable installed dibd path, " +
		"or delete " + unit + " and run `dibs configure --service`; dibs upgrade preserves it"
}

// Missing supervision is fatal to a package's promised setup only when its
// daemon is down. A manual/tutorial daemon is still a supported way to run.
func reportMissingService(what string, serving bool, warn, bad fixFn) {
	installed, err := daemonPath()
	if err != nil || (runtime.GOOS != "darwin" && runtime.GOOS != "linux") {
		return
	}
	fix := "run `dibs configure --service` from your terminal"
	if runtime.GOOS == "linux" {
		fix += "; then run `systemctl --user enable --now dibs`"
	}
	report := warn
	switch {
	case serving:
		fix += ". This daemon has no registered service to bring it back after logout or reboot"
	case homebrewDaemon(installed):
		report = bad
	default:
		fix += ", or start dibd from this terminal"
	}
	report(what, fix)
}

func homebrewDaemon(installed string) bool {
	if resolved, err := filepath.EvalSymlinks(installed); err == nil {
		installed = resolved
	}
	return caskroomPrefix(installed) != ""
}
