// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fault is two files agreeing to disagree, and each is innocent alone.
//
// Claude Desktop's config naming a `dibs` server is the documented install
// route for the chat app. The Claude Code plugin being installed is the
// documented route for the Code tab. Together, the app hands its server to
// Code-tab sessions under the plugin's name and the plugin's server is gone
// from them, so agents register through a bridge spawned from `/` with no
// CLAUDE_PID and bind `host-<pid>`: their hooks then resolve to nobody.
// Measured on 2026-09-15 on the board this is developed on. The two readers
// below are what lets doctor see it, and each must read exactly the field it
// claims to.
func TestDesktopConfigAndPluginRecordAreReadForTheOneFieldEach(t *testing.T) {
	dir := t.TempDir()

	desktop := filepath.Join(dir, "claude_desktop_config.json")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(desktop, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if desktopConfiguresDibs(desktop) {
		t.Error("a missing config must read as not configured, not as a fault")
	}
	write(`{"mcpServers": {}, "preferences": {"dibs": "mentioned but not a server"}}`)
	if desktopConfiguresDibs(desktop) {
		t.Error("the word dibs elsewhere in the file is not a `dibs` server")
	}
	write(`{"mcpServers": {"dibs": {"command": "/usr/local/bin/dibs", "args": ["mcp-stdio"]}}}`)
	if !desktopConfiguresDibs(desktop) {
		t.Error("mcpServers.dibs present and not seen")
	}
	write(`{"mcpServers": {"dibs": `)
	if desktopConfiguresDibs(desktop) {
		t.Error("an unparsable config must not be reported as a fault")
	}

	home := filepath.Join(dir, "home")
	record := filepath.Join(home, ".claude", "plugins", "installed_plugins.json")
	if err := os.MkdirAll(filepath.Dir(record), 0o700); err != nil {
		t.Fatal(err)
	}
	if claudeCodePluginInstalled(home) {
		t.Error("no plugin record means no plugin")
	}
	if err := os.WriteFile(record, []byte(`{"version": 2, "plugins": {"other@dibs": [], "dibs-tools@x": []}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if claudeCodePluginInstalled(home) {
		t.Error("a marketplace named dibs, or a plugin merely starting with the word, is not our plugin")
	}
	if err := os.WriteFile(record, []byte(`{"version": 2, "plugins": {"dibs@dibs": [{"scope": "user"}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !claudeCodePluginInstalled(home) {
		t.Error("dibs@dibs installed and not seen")
	}
}

// Run doctor itself against Claude Code's installed-plugin record, including
// cases where the file supplies no comparable evidence.
func TestDoctorWarnsAboutOlderClaudePlugin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("DIBS_DIR", t.TempDir())
	t.Setenv("DIBS_ADDR", "127.0.0.1:1")
	record := filepath.Join(home, ".claude", "plugins", "installed_plugins.json")
	if err := os.MkdirAll(filepath.Dir(record), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"0.0.9", "100.0.0", "unknown"} {
		t.Run(v, func(t *testing.T) {
			if err := os.WriteFile(record, []byte(`{"version":2,"plugins":{"dibs@dibs":[{"scope":"user","version":"`+v+`"}],"other@dibs":[{"version":"0.0.1"}]}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			out, _ := captureStdout(t, func() error { return doctor([]string{"--json"}) })
			var report doctorReport
			if err := json.Unmarshal([]byte(out), &report); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, check := range report.Checks {
				if check.Level == "warning" && strings.Contains(check.What, "Claude Code") &&
					strings.Contains(check.What, v) && strings.Contains(check.Fix, "claude plugin marketplace update dibs") &&
					strings.Contains(check.Fix, "claude plugin update dibs@dibs") {
					found = true
				}
			}
			if found != (v == "0.0.9") {
				t.Fatalf("Claude plugin version %s: found warning = %v", v, found)
			}
		})
	}
}
