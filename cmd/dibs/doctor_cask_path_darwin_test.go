// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/agenxy/dibs/internal/testexec"
)

// Exercise os.Executable/daemonPath from both entry paths a Mac cask installs.
// An app-internal dibd leads PATH deliberately, but must lose to the adjacent
// staged or brew-linked binary. This installs no cask and calls no app helper.
func TestMacCaskDoctorResolvesThePackageDaemon(t *testing.T) {
	home := t.TempDir()
	stage := filepath.Join(home, "brew", "Caskroom", "dibs", "0.0.17")
	bin := filepath.Join(home, "brew", "bin")
	app := filepath.Join(home, "Applications", "Dibs.app", "Contents", "MacOS")
	board := filepath.Join(home, ".dibs")
	for _, dir := range []string{stage, bin, app, board} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(stage, "dibs")
	if err := os.WriteFile(cli, image, 0o700); err != nil {
		t.Fatal(err)
	}
	stagedDaemon := filepath.Join(stage, "dibd")
	appDaemon := filepath.Join(app, "dibd")
	for _, at := range []string{stagedDaemon, appDaemon} {
		if err := os.WriteFile(at, []byte("private path-discovery fixture; never run\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"dibs", "dibd"} {
		if err := os.Symlink(filepath.Join(stage, name), filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, entry := range []string{cli, filepath.Join(bin, "dibs")} {
		cmd := exec.CommandContext(t.Context(), entry, "-test.run=^TestMacCaskDaemonPathProcess$")
		cmd.Dir = home
		cmd.Env = []string{
			"HOME=" + home, "DIBS_DIR=" + board, "PATH=" + app + ":" + bin,
			"DIBS_TEST_MAC_CASK_PATH=1",
		}
		prepare := exec.Command(entry, "--dibs-test-prepare")
		prepare.Dir, prepare.Env = cmd.Dir, cmd.Env
		testexec.PrepareExecutable(t, prepare)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("path fixture: %v %s", err, out)
		}
		var got struct {
			Daemon  string `json:"daemon"`
			Package bool   `json:"package"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("path fixture JSON: %v %s", err, out)
		}
		resolved, err := filepath.EvalSymlinks(got.Daemon)
		if err != nil || !sameBinary(resolved, stagedDaemon) || !got.Package || sameBinary(got.Daemon, appDaemon) {
			t.Fatalf("entry %s selected non-package daemon: %+v; resolved=%s err=%v", entry, got, resolved, err)
		}
	}
}

func TestMacCaskDaemonPathProcess(t *testing.T) {
	if os.Getenv("DIBS_TEST_MAC_CASK_PATH") != "1" {
		return
	}
	daemon, err := daemonPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"daemon": daemon, "package": homebrewDaemon(daemon)}); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}
