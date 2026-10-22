// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"os"
	"path/filepath"
	"syscall"

	"github.com/agenxy/dibs/internal/machoidentity"
)

// Old installers and manual invocations still enter through the root Mach-O.
// Replace that image in the SAME process so the executing daemon belongs to
// Dibs.app. No environment marker is needed: the bundle location is the guard.
func enterAppDaemon() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return err
	}
	if machoidentity.Bundled(self) {
		return nil
	}
	app, err := machoidentity.PairedApp(self)
	if err != nil || app == "" {
		return err
	}
	paired := filepath.Join(app, "Contents", "MacOS", "dibd")
	// Preserve argv (including argv[0]) and environment byte for byte.
	// #nosec G204,G702 -- own paired app image, checked against this executable's sections.
	return syscall.Exec(paired, os.Args, os.Environ())
}
