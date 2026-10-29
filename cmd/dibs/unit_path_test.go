// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Legacy launchd and bridge units carry the installing shell's PATH, so a wake command named the
// way the documentation names it (`claude`, `codex`) resolves under the
// supervisor as it does in the terminal.
//
// launchd starts a job with `/usr/bin:/bin:/usr/sbin:/sbin` and systemd
// --user with much the same, and neither includes ~/.local/bin or Homebrew,
// which is where both harnesses live. A daemon or host bridge that ran fine
// from the terminal that installed it then failed every wake with "executable
// file not found", while doctor counted the route as covering: the entries
// were present and the bridge advertised them. Found by the pre-release
// review, round four, on the bridge unit; the daemon's unit had the same gap.
func TestUnitsCarryTheInstallingPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	const path = "/Users/me/.local/bin:/opt/homebrew/bin:/usr/bin:/bin"
	t.Setenv("PATH", path)
	dir := filepath.Join(home, "board")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := writeLaunchAgent(filepath.Join(home, "bin", "dibd"), dir); err != nil {
		t.Fatal(err)
	}
	plist, err := os.ReadFile(filepath.Join(home, "Library", "LaunchAgents", "org.agenxy.dibs.plist"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plist), "<key>PATH</key><string>"+path+"</string>") {
		t.Errorf("the daemon's launchd unit does not carry PATH:\n%s", plist)
	}

	if err := writeSystemdUnit(filepath.Join(home, "bin", "dibd"), dir); err != nil {
		t.Fatal(err)
	}
	unit, err := os.ReadFile(filepath.Join(home, ".config", "systemd", "user", "dibs.service"))
	if err != nil {
		t.Fatal(err)
	}
	// Linux package postflights run with brew's private shim PATH. Persisting
	// that broke service execution after installation, so the Linux daemon has
	// defined user/package-manager paths rather than inheriting this shell.
	for _, want := range []string{filepath.Join(home, ".local", "bin"), "/opt/homebrew/bin", "/usr/local/bin"} {
		if !strings.Contains(string(unit), want) {
			t.Errorf("the daemon's systemd unit omits harness directory %q:\n%s", want, unit)
		}
	}
	if strings.Contains(string(unit), "/Users/me/.local/bin") {
		t.Errorf("systemd unit persisted the caller's PATH:\n%s", unit)
	}

	env := map[string]string{"DIBS_ADDR": "https://hub:4777", "DIBS_DIR": dir}
	for _, goos := range []string{"darwin", "linux"} {
		_, body, _, err := bridgeUnit(goos, "/opt/dibs/bin/dibs", dir, unitEnv(env))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(body, path) {
			t.Errorf("the %s host-bridge unit does not carry PATH:\n%s", goos, body)
		}
	}
}
