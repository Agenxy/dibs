// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func commandTestFixture(t *testing.T, name string) (*runner, string) {
	t.Helper()
	root := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(root, name)
	if err := copyCandidateFile(exe, fixture); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(fixture, 0o700); err != nil {
		t.Fatal(err)
	}
	return &runner{ctx: context.Background(), root: root, testExecRoot: root}, fixture
}

func TestCommandUsesChildPATHAndRefusesParentExecutable(t *testing.T) {
	inside, _ := commandTestFixture(t, "brew")
	outside, _ := commandTestFixture(t, "brew")
	t.Setenv("PATH", outside.root)
	env := []string{"PATH=" + inside.root, "DIBS_PUBLIC_COMMAND_FIXTURE=1"}
	_, err := inside.command("child-path-control", env, "brew", "-test.run=^TestPublicCommandExitFixture$")
	if err == nil || len(inside.report.Steps) != 1 || inside.report.Steps[0].Exit != 7 {
		t.Fatalf("child-PATH fixture was not actually executed: %v %+v", err, inside.report.Steps)
	}
	outsideLog := filepath.Join(outside.root, "actual-argv.log")
	outsideEnv := []string{"PATH=" + outside.root, "DIBS_TEST_BREW_CASE=outside-control", "DIBS_TEST_BREW_LOG=" + outsideLog}
	_, err = inside.command("parent-path-refusal", outsideEnv, "brew", "install", "--cask", candidateTap)
	if err == nil || !strings.Contains(err.Error(), "outside the test's private root") {
		t.Fatalf("parent's brew escaped execution containment: %v", err)
	}
	if _, err := os.Stat(outsideLog); !os.IsNotExist(err) {
		t.Fatal("outside executable ran:", err)
	}
}

func TestCommandRefusesRealHostHomebrewBeforeExec(t *testing.T) {
	brew, err := exec.LookPath("brew") // lookup only; the shared runner MUST refuse execution.
	if err != nil {
		t.Skip("no host Homebrew executable to exercise; generic outside-root control still runs")
	}
	r, _ := commandTestFixture(t, "contained-fixture")
	_, err = r.command("host-brew-refusal", []string{"PATH=" + filepath.Dir(brew)}, "brew", "install", "--cask", candidateTap)
	if err == nil || !strings.Contains(err.Error(), "outside the test's private root") {
		t.Fatalf("real host brew was not refused before execution: %v", err)
	}
}

func TestCommandRequiresSentinelAndRejectsSymlinkEscape(t *testing.T) {
	r, exe := commandTestFixture(t, "contained-fixture")
	r.testExecRoot = ""
	if _, err := r.command("missing-sentinel", nil, exe); err == nil || !strings.Contains(err.Error(), "sentinel") {
		t.Fatalf("missing sentinel allowed any execution: %v", err)
	}
	r.testExecRoot = r.root
	_, outside := commandTestFixture(t, "outside-fixture")
	alias := filepath.Join(r.root, "escaped")
	if err := os.Symlink(outside, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := r.command("symlink-escape", nil, alias); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("a lexical test-root path hid an outside executable: %v", err)
	}
}
