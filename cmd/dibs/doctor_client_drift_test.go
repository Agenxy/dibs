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

func TestDoctorWarnsAboutValidOldCodexPluginRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	t.Setenv("DIBS_DIR", t.TempDir())
	t.Setenv("DIBS_ADDR", "127.0.0.1:1")
	root := filepath.Join(home, "plugins", "cache", "dibs", "dibs", "0.0.9")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[plugins.\"dibs@dibs\"]\nenabled = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".mcp.json"), []byte(`{"mcpServers":{"dibs":{"command":"dibs","args":["mcp-stdio"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _ := captureStdout(t, func() error { return doctor([]string{"--json"}) })
	var report doctorReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	for _, check := range report.Checks {
		if check.Level == "warning" && strings.Contains(check.What, "Codex's Dibs plugin") &&
			check.Fix == codexPluginRepair {
			return
		}
	}
	t.Fatal("doctor did not warn about the stale Codex root with its repair command")
}
