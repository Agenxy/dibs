// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/testexec"
)

// A real install through an aliased CODEX_HOME: Codex canonicalizes the receipt
// path, as macOS does for /var -> /private/var. Spelling is not root identity.
func TestCodexPluginReceiptUsesDirectoryIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix directory-symlink receipt regression")
	}
	bin := t.TempDir()
	dibs, codex := filepath.Join(bin, "dibs"), filepath.Join(bin, "codex")
	for _, build := range []struct{ output, source string }{{dibs, "."}, {codex, "./testdata/codexplugin"}} {
		if out, err := exec.Command("go", "build", "-o", build.output, build.source).CombinedOutput(); err != nil {
			t.Fatalf("setup build: %v %s", err, out)
		}
		testexec.PrepareExecutable(t, exec.Command(build.output, "--version"))
	}
	for _, mode := range []string{"canonical-receipt", "other-root-receipt"} {
		t.Run(mode, func(t *testing.T) {
			realHome, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			alias := filepath.Join(t.TempDir(), "codex-home-alias")
			if err := os.Symlink(realHome, alias); err != nil {
				t.Fatal("setup alias:", err)
			}
			if err := os.WriteFile(filepath.Join(realHome, "config.toml"), []byte("[plugins.\"dibs@dibs\"]\nenabled = true\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), dibs, "codex-plugin", "install", "--codex", codex)
			cmd.Env = append(os.Environ(), "CODEX_HOME="+alias, "DIBS_ADMIN=1", "DIBS_PLUGIN_FIXTURE="+mode)
			out, err := cmd.CombinedOutput()
			if _, setupErr := os.Stat(filepath.Join(realHome, "installer-called")); setupErr != nil {
				t.Fatal("setup never reached actual installer:", setupErr)
			}
			_, receiptErr := os.Stat(filepath.Join(realHome, "dibs-marketplace", "receipt.json"))
			if mode == "canonical-receipt" {
				if err != nil || !strings.Contains(string(out), "Codex plugin verified") || receiptErr != nil {
					t.Fatalf("canonical spelling of the same installed directory was refused: %v %s receipt=%v", err, out, receiptErr)
				}
			} else if err == nil || !os.IsNotExist(receiptErr) {
				t.Fatalf("different directory accepted as the installed root: %v %s receipt=%v", err, out, receiptErr)
			}
		})
	}
}
