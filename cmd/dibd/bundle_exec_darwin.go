// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"errors"
	"fmt"
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
	if filepath.Base(filepath.Dir(self)) == "MacOS" &&
		filepath.Base(filepath.Dir(filepath.Dir(self))) == "Contents" {
		return nil
	}
	paired := filepath.Join(filepath.Dir(self), "Dibs.app", "Contents", "MacOS", "dibd")
	info, err := os.Lstat(paired)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} // standalone development image
	if err != nil {
		return fmt.Errorf("paired app daemon: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("paired app daemon %s must be a regular file; rebuild or reinstall the complete Dibs distribution", paired)
	}
	rootID, err := machoidentity.File(self)
	if err != nil {
		return fmt.Errorf("compare root daemon %s sections: %w", self, err)
	}
	appID, err := machoidentity.File(paired)
	if err != nil {
		return fmt.Errorf("compare paired daemon %s sections: %w; rebuild or reinstall the complete Dibs distribution", paired, err)
	}
	if rootID != appID {
		return fmt.Errorf("refusing mismatched paired daemon: root %s = %s; bundle %s = %s; compared Mach-O section names, layout and bytes, excluding signature and load commands. Rebuild or reinstall the complete Dibs distribution", self, rootID, paired, appID)
	}
	// Preserve argv (including argv[0]) and environment byte for byte.
	return syscall.Exec(paired, os.Args, os.Environ())
}
