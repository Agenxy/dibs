// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package machoidentity

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Bundled reports the stable daemon location, without an environment marker
// which an old entry point could accidentally inherit.
func Bundled(executable string) bool {
	macos := filepath.Dir(executable)
	contents := filepath.Dir(macos)
	return filepath.Base(macos) == "MacOS" && filepath.Base(contents) == "Contents" && filepath.Base(filepath.Dir(contents)) == "Dibs.app"
}

// PairedApp resolves the source/archive layout and Homebrew's moved app using
// the same rule for daemon entry and service installation. A present mismatch
// is a refusal, never permission to try an unrelated app of the same name.
func PairedApp(executable string) (string, error) {
	self, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", err
	}
	if Bundled(self) {
		return filepath.Dir(filepath.Dir(filepath.Dir(self))), nil
	}
	candidates := []string{filepath.Join(filepath.Dir(self), "Dibs.app")}
	if strings.Contains(self, string(filepath.Separator)+"Caskroom"+string(filepath.Separator)) {
		candidates = append(candidates, "/Applications/Dibs.app", filepath.Join(os.Getenv("HOME"), "Applications", "Dibs.app"))
	}
	for _, app := range candidates {
		if _, err := os.Stat(app); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return "", err
		}
		paired := filepath.Join(app, "Contents", "MacOS", "dibd")
		info, err := os.Lstat(paired)
		if err != nil {
			return "", fmt.Errorf("paired daemon %s: %w; rebuild or reinstall the complete Dibs distribution", paired, err)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("paired daemon %s must be a regular file; reinstall the complete Dibs distribution", paired)
		}
		rootID, err := File(self)
		if err != nil {
			return "", fmt.Errorf("compare root %s sections: %w", self, err)
		}
		appID, err := File(paired)
		if err != nil {
			return "", fmt.Errorf("compare bundle %s sections: %w; reinstall the complete Dibs distribution", paired, err)
		}
		if rootID != appID {
			return "", fmt.Errorf("refusing mismatched paired daemon: root %s = %s; bundle %s = %s; compared Mach-O section names, layout and bytes, excluding signature and load commands. Rebuild or reinstall the complete Dibs distribution", self, rootID, paired, appID)
		}
		return app, nil
	}
	return "", nil
}
