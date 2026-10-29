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

func checkServiceBinary(ok reportFn, warn, bad fixFn) {
	checkAppService(ok, warn)
	unit, pinned := unitDaemon()
	if unit == "" || pinned == "" {
		return // no service installed: `configure --service` is optional
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
			bad("the service executable does not exist", detail+". Edit ExecStart to the stable installed dibd path, "+
				"or delete "+unit+" and run `dibs configure --service`")
			return
		}
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
