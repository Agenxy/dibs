// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// This guard enters through the shipped command, not a setter or source search.
// The same file on the old commit fails because that command cannot install a
// versioned plugin. Every fixture binary/file belongs to this test's temp root.
func TestCodexPluginCommand(t *testing.T) {
	bin := t.TempDir()
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	dibs := filepath.Join(bin, "dibs"+suffix)
	codex := filepath.Join(bin, "codex-fixture"+suffix)
	for _, build := range []struct{ output, source string }{{dibs, "."}, {codex, "./testdata/codexplugin"}} {
		cmd := exec.Command("go", "build", "-ldflags=-X github.com/agenxy/dibs/internal/build.Version=0.0.14", "-o", build.output, build.source)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build fixture: %v %s", err, out)
		}
	}
	for _, mode := range []string{"dry", "ok", "fail", "late-fail", "partial", "wrong-receipt"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			config := []byte("[plugins.\"dibs@dibs\"]\nenabled = true\n[marketplaces.dibs]\nsource_type = \"local\"\nsource = \"/original/checkout\"\n")
			if err := os.WriteFile(filepath.Join(home, "config.toml"), config, 0o600); err != nil {
				t.Fatal(err)
			}
			oldRoot := filepath.Join(home, "plugins", "cache", "dibs", "dibs", "0.0.9")
			if err := os.MkdirAll(oldRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			old := []byte("old plugin bytes\n")
			if err := os.WriteFile(filepath.Join(oldRoot, ".mcp.json"), old, 0o640); err != nil {
				t.Fatal(err)
			}
			args := []string{"codex-plugin", "install", "--codex", codex}
			if mode == "dry" {
				args = append(args, "--dry-run")
			}
			cmd := exec.CommandContext(t.Context(), dibs, args...)
			cmd.Env = append(os.Environ(), "CODEX_HOME="+home, "DIBS_ADMIN=1", "DIBS_PLUGIN_FIXTURE="+mode)
			out, err := cmd.CombinedOutput()
			if mode == "ok" || mode == "dry" {
				if err != nil {
					t.Fatalf("command failed: %v %s", err, out)
				}
				if !strings.Contains(string(out), "build-0.0.14-tools-") || !strings.Contains(string(out), "running chats retain their tool list") && !strings.Contains(string(out), "Running chats retain their existing tool list") {
					t.Fatalf("missing version/boundary: %s", out)
				}
			} else if err == nil {
				t.Fatal("unverified installer failure reported success")
			}
			if mode != "dry" {
				if _, err := os.Stat(filepath.Join(home, "installer-called")); err != nil {
					t.Fatal("probe never reached the fixture installer:", err)
				}
			}
			gotConfig, err := os.ReadFile(filepath.Join(home, "config.toml"))
			if err != nil || string(gotConfig) != string(config) {
				t.Fatal("installer changed the persistent marketplace source/config")
			}
			base := filepath.Dir(oldRoot)
			entries, err := os.ReadDir(base)
			if err != nil || len(entries) != 1 {
				t.Fatalf("ambiguous cache: %v %v", entries, err)
			}
			if mode != "ok" {
				got, err := os.ReadFile(filepath.Join(oldRoot, ".mcp.json"))
				info, statErr := os.Stat(filepath.Join(oldRoot, ".mcp.json"))
				if err != nil || string(got) != string(old) || statErr != nil ||
					runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
					t.Fatalf("prior plugin not preserved/restored: %v %v", err, statErr)
				}
				if _, err := os.Stat(filepath.Join(home, "dibs-marketplace", "receipt.json")); !os.IsNotExist(err) {
					t.Fatal("failed/dry installer advanced verified receipt")
				}
				if mode == "dry" {
					if _, err := os.Stat(filepath.Join(home, "dibs-marketplace")); !os.IsNotExist(err) {
						t.Fatal("dry run wrote materialization or metadata")
					}
					if _, err := os.Stat(filepath.Join(home, "installer-called")); !os.IsNotExist(err) {
						t.Fatal("dry run invoked installer")
					}
				}
				// Enter doctor's shipped command too. It must identify the stale
				// plugin even when an unrelated missing daemon secret exits early.
				doctor := exec.CommandContext(t.Context(), dibs, "doctor", "--json")
				doctor.Env = append(append([]string{}, cmd.Env...), "DIBS_DIR="+filepath.Join(home, "absent-board"))
				out, _ := doctor.CombinedOutput()
				var report struct {
					Checks []struct{ Level, Fix string } `json:"checks"`
				}
				if err := json.Unmarshal(out, &report); err != nil {
					t.Fatalf("doctor returned no JSON report: %v %s", err, out)
				}
				found := false
				for _, check := range report.Checks {
					found = found || check.Level == "problem" && check.Fix == "dibs codex-plugin install"
				}
				if !found {
					t.Fatal("doctor did not report stale/unverified plugin with repair command")
				}
				return
			}
			root := filepath.Join(base, entries[0].Name())
			var stamp map[string]string
			b, err := os.ReadFile(filepath.Join(root, ".dibs-tools.json"))
			if err != nil || json.Unmarshal(b, &stamp) != nil || stamp["build"] != "0.0.14" || stamp["tools_hash"] == "" {
				t.Fatal("installed version lacks current tool/build stamp")
			}
			b, err = os.ReadFile(filepath.Join(root, ".mcp.json"))
			var servers struct {
				Servers map[string]struct{ Command string } `json:"mcpServers"`
			}
			if err != nil || json.Unmarshal(b, &servers) != nil || servers.Servers["dibs"].Command != dibs ||
				!strings.Contains(string(b), "2026-07-28") {
				t.Fatal("installed plugin does not launch this build with MCP 2026")
			}
			if err := os.Remove(filepath.Join(home, "installer-called")); err != nil {
				t.Fatal(err)
			}
			// A verified same-version repair must not rerun the installer, and
			// must clear an earlier diagnostic without claiming a loaded refresh.
			marker := filepath.Join(home, "dibs-marketplace", "refresh-failed")
			if err := os.WriteFile(marker, []byte("old failure"), 0o600); err != nil {
				t.Fatal(err)
			}
			repeat := exec.CommandContext(t.Context(), dibs, args...)
			repeat.Env = cmd.Env
			if out, err := repeat.CombinedOutput(); err != nil {
				t.Fatalf("repeat install: %v %s", err, out)
			}
			if _, err := os.Stat(filepath.Join(home, "installer-called")); !os.IsNotExist(err) {
				t.Fatal("verified same-version installation reran installer")
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("verified repair retained failure marker")
			}
		})
	}
}
