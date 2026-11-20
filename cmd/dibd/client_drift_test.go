// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/procexec"
)

func TestDaemonWarnsAboutLocalClientVersions(t *testing.T) {
	home, path := t.TempDir(), t.TempDir()
	stale := filepath.Join(path, "dibs")
	const stamp = "-X github.com/agenxy/dibs/internal/build.Version=0.0.5"
	if out, err := procexec.Command(t.Context(), "go", "build", "-ldflags", stamp, "-o", stale, "../dibs").CombinedOutput(); err != nil {
		t.Fatalf("setup: build a stamped CLI: %v %s", err, out)
	}
	write := func(name, body string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(home, ".claude", "plugins", "installed_plugins.json"),
		`{"version":2,"plugins":{"dibs@dibs":[{"scope":"user","version":"0.0.9"}]}}`, 0o600)
	codex := filepath.Join(home, ".codex")
	write(filepath.Join(codex, "config.toml"), "[plugins.\"dibs@dibs\"]\nenabled = true\n", 0o600)
	write(filepath.Join(codex, "plugins", "cache", "dibs", "dibs", "0.0.9", ".mcp.json"),
		`{"mcpServers":{"dibs":{"command":"dibs"}}}`, 0o600)
	other := filepath.Join(path, "dibd")
	write(other, "not a Go executable", 0o700)
	_, stop, logs := qStartBoardCaptured(t, qShortDir(t),
		"HOME="+home, "CODEX_HOME="+codex, "PATH="+path+string(os.PathListSeparator)+os.Getenv("PATH"),
		"DIBS_TEST_DAEMON_BUILD=0.0.19", "DIBS_NOTIFY=off")
	wants := []string{
		"Claude Code's Dibs plugin includes older versions (0.0.9)",
		"claude plugin marketplace update dibs", "claude plugin update dibs@dibs",
		"Codex's Dibs plugin does not have a verified current version root", "dibs codex-plugin install",
		stale + " is an older Dibs binary (0.0.5)",
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if strings.Contains(logs(), wants[len(wants)-1]) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	for _, want := range wants {
		if !strings.Contains(logs(), want) {
			t.Errorf("daemon startup omitted %q: %s", want, logs())
		}
	}
	if strings.Contains(logs(), other+" is an older Dibs binary") {
		t.Fatal("PATH inspection misidentified an unrelated executable as Dibs")
	}
}
