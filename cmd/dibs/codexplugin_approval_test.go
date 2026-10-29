// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/testexec"
)

func TestPluginApprovalProbeReadsTheInstallerHome(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "codex-fixture")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", bin, "./testdata/codexplugin").CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	testexec.PrepareExecutable(t, exec.Command(bin, "--version"))
	processHome, installedHome := t.TempDir(), t.TempDir()
	t.Setenv("CODEX_HOME", processHome)
	t.Setenv("DIBS_HOOK_FIXTURE_STATUS", "modified")
	p := &codexPluginPlan{home: installedHome, bin: bin}
	var out bytes.Buffer
	p.reportHookApproval(&out)
	if !strings.Contains(out.String(), "dibs codex-hooks --trust") {
		t.Errorf("probe omitted real approval result: %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(installedHome, "hooks-listed")); err != nil {
		t.Fatal("probe did not read the installer home:", err)
	}
	if _, err := os.Stat(filepath.Join(processHome, "hooks-listed")); !os.IsNotExist(err) {
		t.Error("probe borrowed unrelated process-global Codex state")
	}
}
