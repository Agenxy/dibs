// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package testexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
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

// A fixture still held open for writing by another process is refused by
// Linux execve with ETXTBSY. In the gate that holder is a sibling test's fork,
// for the instant before it execs; here it is this test, which lets go after a
// moment. Preparation must wait it out rather than fail the caller's setup.
func TestPrepareExecutableWaitsOutABusyExecutable(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("ETXTBSY is how Linux execve refuses a file open for writing")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fixture")
	writer, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	if _, err := writer.Write(data); err != nil {
		t.Fatal(err)
	}
	// The setup has to produce the refusal, or this passes for nothing.
	if err := exec.Command(path, "-test.run=^$").Run(); !errors.Is(err, syscall.ETXTBSY) {
		t.Fatalf("setup: launching a file open for writing returned %v, want ETXTBSY", err)
	}
	released := make(chan struct{})
	time.AfterFunc(200*time.Millisecond, func() {
		_ = writer.Close()
		close(released)
	})
	command := exec.Command(path, "-test.run=^TestPrepareExecutableFixture$")
	command.Env = append(os.Environ(), "GORACE=atexit_sleep_ms=0")
	PrepareExecutable(t, command)
	<-released
}
