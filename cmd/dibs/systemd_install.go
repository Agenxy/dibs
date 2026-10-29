// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/agenxy/dibs/internal/paths"
)

const systemdReceipt = "# written by dibs configure --service; sha256 of the rest: "

func systemdBody(daemon, dir, searchPath string) string {
	return `[Unit]
Description=Dibs: coordination board for AI agents
Documentation=https://github.com/agenxy/dibs
After=network.target

[Service]
Type=simple
` + systemdEnvironment(map[string]string{"PATH": searchPath}) + `ExecStart=` +
		systemdArg(daemon) + ` -dir ` + systemdArg(dir) + `
Restart=on-failure
RestartSec=2

[Install]
WantedBy=default.target
`
}

// A receipt describes the rest of this same file: it cannot be detached from
// its unit. A hand edit invalidates the digest even if it keeps the comment.
func generatedSystemdUnit(body string) bool {
	header, rest, found := strings.Cut(body, "\n")
	return found && header == fmt.Sprintf("%s%x", systemdReceipt, sha256.Sum256([]byte(rest)))
}

func systemdDataDir(body string) string {
	for _, line := range strings.Split(body, "\n") {
		command, found := strings.CutPrefix(strings.TrimSpace(line), "ExecStart=")
		if !found {
			continue
		}
		args := systemdTokens(command)
		for i := 1; i+1 < len(args); i++ {
			if args[i] == "-dir" {
				return filepath.Clean(args[i+1])
			}
		}
	}
	return ""
}

func caskroomPrefix(binary string) string {
	versionDir := filepath.Dir(binary)
	packageDir := filepath.Dir(versionDir)
	room := filepath.Dir(packageDir)
	if filepath.IsAbs(binary) && filepath.Base(binary) == "dibd" &&
		filepath.Base(packageDir) == "dibs" && filepath.Base(room) == "Caskroom" {
		return filepath.Dir(room)
	}
	return ""
}

// Match the published v0.0.16 template byte for byte. The original shim PATH
// is part of that evidence, not a new default. Anything edited is preserved.
func legacyBrewSystemdUnit(target, body, dir string) bool {
	daemon := unitBinary(target)
	prefix := caskroomPrefix(daemon)
	if prefix == "" {
		return false
	}
	oldPath := filepath.Join(prefix, "Homebrew", "Library", "Homebrew", "shims", "shared") +
		":" + paths.DefaultServicePath
	return body == systemdBody(daemon, dir, oldPath)
}

func systemdSearchDirs(daemon string) []string {
	// Deliberately independent of the postflight's PATH. Homebrew's internal
	// shims are temporary execution machinery, not a daemon search path.
	dirs := []string{
		filepath.Dir(daemon), filepath.Join(os.Getenv("HOME"), ".local", "bin"),
		filepath.Join(os.Getenv("HOME"), ".linuxbrew", "bin"), "/home/linuxbrew/.linuxbrew/bin",
		"/opt/homebrew/bin", "/usr/local/bin", "/usr/local/sbin",
	}
	dirs = append(dirs, strings.Split(paths.DefaultServicePath, ":")...)
	var out []string
	for _, dir := range dirs {
		if filepath.IsAbs(dir) && !slices.Contains(out, dir) {
			out = append(out, dir)
		}
	}
	return out
}

func stableSystemdDaemon(daemon string) (string, error) {
	if prefix := caskroomPrefix(daemon); prefix != "" {
		stable := filepath.Join(prefix, "bin", "dibd")
		if !sameBinary(stable, daemon) {
			return "", fmt.Errorf("%s does not resolve to the installed dibd; repair the package-manager link "+
				"with `brew reinstall dibs` before configuring its service", stable)
		}
		return stable, nil // keep the link's spelling, never its versioned target
	}
	for _, dir := range systemdSearchDirs(daemon) {
		candidate := filepath.Join(dir, "dibd")
		if candidate != daemon && sameBinary(candidate, daemon) {
			return candidate, nil
		}
	}
	return daemon, nil
}

func preserveSystemdUnit(target string) {
	fmt.Printf("preserved %s: it has local changes or names another board; no file was changed\n", target)
	if pinned := unitBinary(target); pinned != "" {
		if _, err := os.Stat(pinned); os.IsNotExist(err) {
			fmt.Printf("WARNING: ExecStart names %s, which does not exist. Edit ExecStart to the installed "+
				"stable dibd path, or delete %s and run `dibs configure --service`\n", pinned, target)
		}
	}
}

func installSystemdUnit(daemon, dir string) error {
	unitDir := filepath.Join(configHome(), "systemd", "user")
	if err := refuseIfLegacySystemdUnitExists(unitDir); err != nil {
		return err
	}
	target := filepath.Join(unitDir, "dibs.service")
	// #nosec G304 G703 -- operator's own config directory and fixed unit name
	old, err := os.ReadFile(target)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading %s before configure: %w; check its permissions", target, err)
	}
	exists := err == nil
	managed := generatedSystemdUnit(string(old)) && systemdDataDir(string(old)) == filepath.Clean(dir)
	if exists && !replaceUnits && !managed && !legacyBrewSystemdUnit(target, string(old), dir) {
		preserveSystemdUnit(target)
		return nil
	}
	stable, err := stableSystemdDaemon(daemon)
	if err != nil {
		return err
	}
	rest := systemdBody(stable, dir, strings.Join(systemdSearchDirs(stable), ":"))
	unit := fmt.Sprintf("%s%x\n%s", systemdReceipt, sha256.Sum256([]byte(rest)), rest)
	if string(old) == unit {
		fmt.Printf("already current: %s\n", target)
		return nil
	}
	// Preservation and legacy-unit refusals above create no board directory.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if replaceUnits {
		// Explicit upgrade retains its existing backup/replacement contract.
		err = writeUnit(target, unit)
	} else {
		err = writeGeneratedSystemdUnit(target, unit, old, exists)
	}
	if err != nil {
		return err
	}
	fmt.Printf("wrote %s\n\nEnable and start it:\n  systemctl --user enable --now dibs\n\n"+
		"Logs: journalctl --user -u dibs -f\n", target)
	sayWhenTheUnitComesBack()
	return nil
}

func writeGeneratedSystemdUnit(target, body string, old []byte, exists bool) error {
	// #nosec G703 -- target is the fixed unit in this user's config directory
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if exists {
		// Keep permissions chosen for the existing unit as well as its contents.
		// #nosec G703 -- the configured unit whose bytes are checked before replacement
		info, err := os.Stat(target)
		if err != nil {
			return err
		}
		mode = info.Mode().Perm()
	}
	// #nosec G703 -- temporary file beside the operator's configured unit
	file, err := os.CreateTemp(filepath.Dir(target), ".dibs-service-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return err
	}
	_, writeErr := file.WriteString(body)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if !exists {
		// Link installs a complete new file without overwriting a unit that
		// appeared during configure. Both paths are in the same directory.
		// #nosec G703 -- configured unit and its private temporary file
		return os.Link(file.Name(), target)
	}
	// #nosec G304 G703 -- same configured unit, recheck immediately before replacement
	current, err := os.ReadFile(target)
	if err != nil || string(current) != string(old) {
		return fmt.Errorf("%s changed during configure; preserved it, run configure again after editing", target)
	}
	// #nosec G703 -- same configured directory, atomic managed-unit replacement
	return os.Rename(file.Name(), target)
}
