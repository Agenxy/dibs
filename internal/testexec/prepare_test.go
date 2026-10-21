// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package testexec

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestPrepareExecutableFixture(t *testing.T) {
	switch os.Getenv("DIBS_TEST_PREPARE_MODE") {
	case "ok":
		if err := os.WriteFile("prepared", []byte(os.Getenv("DIBS_TEST_PREPARE_VALUE")), 0o600); err != nil {
			t.Fatal(err)
		}
	case "fail":
		os.Exit(7)
	case "hang":
		<-time.After(time.Hour)
	case "orphan":
		child := exec.Command(os.Args[0], "-test.run=^TestPrepareExecutableFixture$")
		child.Env = append(os.Environ(), "DIBS_TEST_PREPARE_MODE=orphan-child")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
	case "orphan-child":
		<-time.After(3 * time.Second)
	}
}

func TestPrepareExecutablePreservesEnvironmentAndDirectory(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	command := exec.Command(self, "-test.run=^TestPrepareExecutableFixture$")
	command.Dir = dir
	command.Env = append(os.Environ(), "DIBS_TEST_PREPARE_MODE=ok", "DIBS_TEST_PREPARE_VALUE=private fixture", "GORACE=atexit_sleep_ms=0")
	PrepareExecutable(t, command)
	data, err := os.ReadFile(dir + string(os.PathSeparator) + "prepared")
	if err != nil || string(data) != "private fixture" {
		t.Fatalf("preparation did not run with its intended environment/directory: %q %v", data, err)
	}
}

func TestPrepareExecutableRefusesFailedOrStuckSetup(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if mode := os.Getenv("DIBS_TEST_PREPARE_DRIVER"); mode != "" {
		command := exec.Command(self, "-test.run=^TestPrepareExecutableFixture$")
		command.Env = append(os.Environ(), "DIBS_TEST_PREPARE_MODE="+mode, "GORACE=atexit_sleep_ms=0")
		PrepareExecutable(t, command)
		return
	}
	for _, mode := range []string{"fail", "hang", "orphan"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, self, "-test.run=^TestPrepareExecutableRefusesFailedOrStuckSetup$")
			command.Env = append(os.Environ(), "DIBS_TEST_PREPARE_DRIVER="+mode, "GORACE=atexit_sleep_ms=0")
			out, err := command.CombinedOutput()
			if err == nil || ctx.Err() != nil || !strings.Contains(string(out), "executable fixture setup:") || !strings.Contains(string(out), "bound 10s") {
				t.Fatalf("%s setup was not refused by the bounded helper: err=%v context=%v output=%s", mode, err, ctx.Err(), out)
			}
			if mode == "fail" && !strings.Contains(string(out), "exit status 7") {
				t.Fatalf("setup failure hid child exit: %s", out)
			}
		})
	}
}
