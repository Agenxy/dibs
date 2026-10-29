// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/testexec"
)

// Real command doors, with only Codex itself replaced by the existing private
// installer fixture. A current verified cache must not hide a conflicting
// persisted source: the next real Codex sync would replace that cache again.
func TestCodexPluginSourceChoiceThroughCommands(t *testing.T) {
	bin := t.TempDir()
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	dibs, codex := filepath.Join(bin, "dibs"+suffix), filepath.Join(bin, "codex"+suffix)
	for _, build := range []struct{ output, source string }{{dibs, "."}, {codex, "./testdata/codexplugin"}} {
		if out, err := exec.Command("go", "build", "-o", build.output, build.source).CombinedOutput(); err != nil {
			t.Fatalf("setup build: %v %s", err, out)
		}
		testexec.PrepareExecutable(t, exec.Command(build.output, "--version"))
	}
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "verified-cache", true: "stale-cache"}[stale], func(t *testing.T) {
			home := t.TempDir()
			if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[plugins.\"dibs@dibs\"]\nenabled = true\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			env := append(os.Environ(), "CODEX_HOME="+home, "DIBS_ADMIN=1", "DIBS_PLUGIN_FIXTURE=ok", "DIBS_DIR="+filepath.Join(home, "absent-board"))
			call := func(args ...string) ([]byte, error) {
				t.Helper()
				cmd := exec.CommandContext(t.Context(), dibs, args...)
				cmd.Env = env
				return cmd.CombinedOutput()
			}
			if out, err := call("codex-plugin", "install", "--codex", codex); err != nil {
				t.Fatalf("setup install: %v %s", err, out)
			}
			before, _ := call("doctor", "--json")
			if !strings.Contains(string(before), "current verified version root") {
				t.Fatalf("setup did not establish verified plugin: %s", before)
			}
			configPath := filepath.Join(home, "config.toml")
			config, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			chosen := filepath.Join(home, "person's chosen checkout")
			quoted, _ := json.Marshal(chosen)
			config = append(config, []byte("\n[marketplaces.dibs]\nsource_type = \"local\"\nsource = "+string(quoted)+"\n")...)
			if err := os.WriteFile(configPath, config, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(home, "installer-called")); err != nil {
				t.Fatal(err)
			}
			if stale {
				if err := os.Remove(filepath.Join(home, "dibs-marketplace", "receipt.json")); err != nil {
					t.Fatal(err)
				}
			}
			root := filepath.Join(home, "plugins", "cache", "dibs", "dibs")
			original := pluginSourceTree(t, root)
			for _, args := range [][]string{{"codex-plugin", "install", "--codex", codex}, {"codex-plugin", "install", "--codex", codex, "--dry-run"}} {
				out, err := call(args...)
				if err == nil || !strings.Contains(string(out), chosen) || !strings.Contains(string(out), "codex plugin marketplace remove dibs") || !strings.Contains(string(out), codexPluginRepair) {
					t.Errorf("source conflict was not refused with the exact source and corrective pair: err=%v %s", err, out)
				}
			}
			out, _ := call("doctor", "--json")
			var report doctorReport
			if err := json.Unmarshal(out, &report); err != nil {
				t.Fatalf("doctor JSON: %v %s", err, out)
			}
			found := false
			for _, check := range report.Checks {
				if check.Level == "problem" && strings.Contains(check.What, chosen) && strings.Contains(check.Fix, "codex plugin marketplace remove dibs") && strings.Contains(check.Fix, codexPluginRepair) {
					found = true
				}
			}
			if !found {
				t.Errorf("doctor missed the persisted source conflict: %s", out)
			}
			if got, err := os.ReadFile(configPath); err != nil || !bytes.Equal(got, config) {
				t.Fatal("person's marketplace choice was rewritten")
			}
			if got := pluginSourceTree(t, root); got != original {
				t.Fatal("conflict mutated the installed cache before refusal")
			}
			if _, err := os.Stat(filepath.Join(home, "installer-called")); !os.IsNotExist(err) {
				t.Fatal("conflict invoked the installer")
			}
		})
	}
}

func pluginSourceTree(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			b.WriteString(path)
			b.Write(data)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// Exercise main's actual already-current-daemon upgrade path. Source-choice
// refusal must neither run the installer nor withhold an independent daemon fix.
func TestActualUpgradeExplainsCodexSourceConflictAndContinues(t *testing.T) {
	cmd, home := currentDaemonUpgradePluginFixture(t)
	chosen := filepath.Join(home, "chosen-checkout")
	quoted, _ := json.Marshal(chosen)
	configPath := filepath.Join(home, "config.toml")
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	config = append(config, []byte("\n[marketplaces.dibs]\nsource_type = \"local\"\nsource = "+string(quoted)+"\n")...)
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("source conflict blocked daemon upgrade: %v %s", err, out)
	}
	for _, want := range []string{chosen, "codex plugin marketplace remove dibs", codexPluginRepair, "Continuing the daemon upgrade", "nothing to do"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("actual upgrade missed %q: %s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "installer-called")); !os.IsNotExist(err) {
		t.Error("upgrade invoked installer despite the conflicting source")
	}
	t.Setenv("CODEX_HOME", home)
	d := &diagnosis{json: true}
	d.checkCodexPluginVersion()
	if len(d.checks) != 1 || d.checks[0].Level != "problem" || !strings.Contains(d.checks[0].What, chosen) || !strings.Contains(string(out), d.checks[0].What) {
		t.Errorf("doctor and upgrade do not share the exact source diagnosis: %+v; %s", d.checks, out)
	}
	if got, err := os.ReadFile(configPath); err != nil || !bytes.Equal(config, got) {
		t.Fatal("upgrade changed the person's marketplace source")
	}
}
