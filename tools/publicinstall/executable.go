// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

func (r *runner) resolveExecutable(env, args []string) (string, error) {
	if len(args) == 0 || args[0] == "" {
		return "", errors.New("command requires an executable")
	}
	path, err := executableInChildPath(args[0], env)
	if err != nil {
		return "", err
	}
	if flag.Lookup("test.v") != nil {
		if err := containedTestExecutable(r.testExecRoot, path); err != nil {
			return "", err
		}
		return path, nil
	}
	if runtime.GOOS == "darwin" && filepath.Base(path) == "brew" && mutatesHomebrew(args[1:]) {
		if err := refuseRegisteredMacCask(); err != nil {
			return "", err
		}
	}
	return path, nil
}

func executableInChildPath(name string, env []string) (string, error) {
	if filepath.IsAbs(name) {
		return exec.LookPath(name)
	}
	if strings.ContainsAny(name, `/\`) {
		return "", errors.New("command executable must be absolute or a bare child-PATH name")
	}
	var search string
	for _, value := range env {
		if path, ok := strings.CutPrefix(value, "PATH="); ok {
			search = path // exec.Cmd uses the last environment value.
		}
	}
	for _, dir := range filepath.SplitList(search) {
		if !filepath.IsAbs(dir) {
			continue // never current-directory or parent-PATH fallback.
		}
		if path, err := exec.LookPath(filepath.Join(dir, name)); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("executable %q is absent from the child's absolute PATH", name)
}

func containedTestExecutable(root, executable string) error {
	if root == "" || !filepath.IsAbs(root) {
		return errors.New("under go test, command execution requires the test helper's private-root sentinel")
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	realExecutable, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(realRoot, realExecutable)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("under go test, refusing executable outside the test's private root")
	}
	return nil
}

func mutatesHomebrew(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "install", "uninstall", "reinstall", "upgrade", "tap", "untap", "update":
		return true
	default:
		return false
	}
}

// There is no Mac-cask mode in validateMode; every real cask mode already
// requires native ephemeral Linux. Keep this final guard at the shared exec
// boundary too, so adding a new mode cannot silently register over this board.
func refuseRegisteredMacCask() error {
	for _, domain := range []string{"gui/" + strconv.Itoa(os.Geteuid()), "system"} {
		// #nosec G204 -- fixed launchctl query, numeric euid, fixed service label; no authorization request
		out, err := exec.Command("/bin/launchctl", "print", domain+"/org.agenxy.dibs.daemon").CombinedOutput()
		if err == nil {
			return errors.New("refusing host-mutating Homebrew on a Mac with org.agenxy.dibs.daemon registered; " +
				"use hosted CI or an isolated Quarter, never replace the production service")
		}
		missingService := strings.Contains(string(out), "Could not find service")
		missingDomain := strings.Contains(string(out), "Could not find domain")
		if !missingService && !missingDomain {
			return errors.New("cannot establish that this Mac has no registered Dibs daemon; " +
				"inspect launchctl before using hosted CI or an isolated Quarter")
		}
	}
	return nil
}
