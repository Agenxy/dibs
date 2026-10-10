// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestUpgradePluginFailureIsDiagnosedAndDoesNotBlock(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "codex-fixture")
	if out, err := exec.Command("go", "build", "-o", bin, "./testdata/codexplugin").CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v %s", err, out)
	}
	oldDiscovery := codexPluginExecutable
	t.Cleanup(func() { codexPluginExecutable = oldDiscovery })
	codexPluginExecutable = func() string { return bin }
	for _, mode := range []string{"enabled", "disabled", "absent", "dry", "missing-cli"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CODEX_HOME", home)
			t.Setenv("DIBS_PLUGIN_FIXTURE", "late-fail")
			codexPluginExecutable = func() string { return bin }
			if mode == "missing-cli" {
				codexPluginExecutable = func() string { return "" }
			}
			if mode != "absent" {
				enabled := "true"
				if mode == "disabled" {
					enabled = "false"
				}
				if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[plugins.\"dibs@dibs\"]\nenabled = "+enabled+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				root := filepath.Join(home, "plugins", "cache", "dibs", "dibs", "0.0.9")
				if err := os.MkdirAll(root, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, ".mcp.json"), []byte("old"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			// Enter the same upgrade operation used before the daemon no-op and
			// cutover decisions. It returns normally on installer failure.
			refreshUpgradeCodexPlugin(&out, mode == "dry")
			if mode == "enabled" || mode == "missing-cli" {
				if !strings.Contains(out.String(), "Continuing the daemon upgrade") || !strings.Contains(out.String(), codexPluginRepair) {
					t.Fatalf("missing independent-upgrade warning/repair: %s", out.String())
				}
				d := &diagnosis{json: true}
				d.checkCodexPluginVersion()
				if d.probs != 1 || d.checks[0].Fix != codexPluginRepair || doctorResult(d.probs, d.warns) == nil {
					t.Fatal("plugin failure is not a doctor error with the repair command")
				}
			} else {
				if _, err := os.Stat(filepath.Join(home, "dibs-marketplace")); !os.IsNotExist(err) {
					t.Fatal("absent/disabled/dry integration was mutated")
				}
				if _, err := os.Stat(filepath.Join(home, "installer-called")); !os.IsNotExist(err) {
					t.Fatal("absent/disabled/dry integration ran installer")
				}
			}
		})
	}
}

func TestCodexPluginChangedBuildUsesDifferentRoots(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	first, err := planCodexPlugin("not-run")
	if err != nil {
		t.Fatal(err)
	}
	oldVersion := version
	t.Cleanup(func() { version = oldVersion })
	version = oldVersion + ".next"
	second, err := planCodexPlugin("not-run")
	if err != nil {
		t.Fatal(err)
	}
	if first.version == second.version || first.source == second.source {
		t.Fatal("changed build reuses the same materialized version root")
	}
	// The real tools fingerprint is represented in both the installed manifest
	// root and the self-describing payload stamp; it is not a receipt-only claim.
	if !strings.Contains(first.files[".codex-plugin/plugin.json"], first.version) ||
		!strings.Contains(first.version, "-tools-") || !strings.Contains(first.files[".dibs-tools.json"], "tools_hash") {
		t.Fatal("versioned payload does not carry schema identity")
	}
}

// This enters main's actual upgrade command, reaches planUpgrade/preflight and
// the already-on branch, and proves the plugin job is wired before that branch.
// Removing the refresh call in upgrade makes this guard fail, even though all
// direct installer tests still pass.
func TestActualCurrentDaemonUpgradeAttemptsPluginAndContinuesOnFailure(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "dibd"+suffix), b, 0o700); err != nil {
		t.Fatal(err)
	}
	codex := filepath.Join(binDir, "codex-fixture"+suffix)
	if out, err := exec.Command("go", "build", "-o", codex, "./testdata/codexplugin").CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v %s", err, out)
	}
	home := filepath.Join(root, "codex-home")
	oldRoot := filepath.Join(home, "plugins", "cache", "dibs", "dibs", "0.0.9")
	if err := os.MkdirAll(oldRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldRoot, ".mcp.json"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[plugins.\"dibs@dibs\"]\nenabled = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestCLIUpgradeRecordProcess$", "--", "upgrade")
	cmd.Env = append(os.Environ(), "DIBS_TEST_UPGRADE_RECORD=1", "DIBS_TEST_UPGRADE_BARE=1",
		"DIBS_TEST_PLUGIN_EXECUTABLE="+codex, "DIBS_TEST_PLUGIN_HOME="+home, "DIBS_PLUGIN_FIXTURE=late-fail",
		"DIBS_DIR="+root, "DIBS_ADDR=127.0.0.1:49998", "HOME="+root, "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"), "DIBS_NOTIFY=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("actual current-daemon upgrade was blocked by plugin failure: %v %s", err, out)
	}
	for _, want := range []string{"Continuing the daemon upgrade", codexPluginRepair, "nothing to do"} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("actual upgrade missed %q: %s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "installer-called")); err != nil {
		t.Fatal("actual upgrade never reached the plugin installer:", err)
	}
	if _, err := os.Stat(filepath.Join(home, "dibs-marketplace", "refresh-failed")); err != nil {
		t.Fatal("actual upgrade did not retain doctor failure diagnosis:", err)
	}
}
