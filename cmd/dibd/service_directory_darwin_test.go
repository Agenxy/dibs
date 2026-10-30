// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestMalformedServicePinRefusesBeforeDaemonCreatesABoard(t *testing.T) {
	home := t.TempDir()
	pin := filepath.Join(home, "Library", "Application Support", "org.agenxy.dibs", "service.toml")
	if err := os.MkdirAll(filepath.Dir(pin), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pin, []byte("version=999\ndata_dir='/never-open-this'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("DIBS_DIR", "")
	t.Setenv("DIBS_TEST_SERVICE_DIRECTORY_PROCESS", "1")
	out, err := exec.Command(os.Args[0], "-test.run=^TestServiceDirectoryStartupProcess$").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "cannot select the Dibs board") {
		t.Errorf("actual daemon did not refuse the pin: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(home, ".dibs")); !os.IsNotExist(err) {
		t.Fatalf("refused startup created a default board directory: %v", err)
	}
}

func TestServiceDirectoryStartupProcess(t *testing.T) {
	t.Helper()
	if os.Getenv("DIBS_TEST_SERVICE_DIRECTORY_PROCESS") != "1" {
		return
	}
	flag.CommandLine = flag.NewFlagSet("dibd", flag.ContinueOnError)
	os.Args = []string{"dibd", "-check"}
	if os.Getenv("DIBS_TEST_APP_SERVICE") == "1" {
		os.Args = append(os.Args, "-app-service")
	}
	main()
}

func TestAppServiceRequiresItsPinBeforeCreatingAnyDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DIBS_DIR", "")
	t.Setenv("DIBS_TEST_SERVICE_DIRECTORY_PROCESS", "1")
	t.Setenv("DIBS_TEST_APP_SERVICE", "1")
	child := func() (string, error) {
		out, err := exec.Command(os.Args[0], "-test.run=^TestServiceDirectoryStartupProcess$").CombinedOutput()
		return string(out), err
	}
	if out, err := child(); err == nil || !strings.Contains(out, "required service pin is missing") {
		t.Fatalf("missing pin was not refused: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(home, ".dibs")); !os.IsNotExist(err) {
		t.Fatal("missing service pin created default directory:", err)
	}
	// A valid pin must reach the real replay preflight at the selected board.
	// An empty fixture has no ledger; reaching this diagnostic proves setup.
	dir := filepath.Join(home, "selected-board")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	pin := filepath.Join(home, "Library", "Application Support", "org.agenxy.dibs", "service.toml")
	if err := os.MkdirAll(filepath.Dir(pin), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pin, []byte("version=1\ndata_dir="+strconv.Quote(dir)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := child(); err == nil || !strings.Contains(out, "no board at "+dir) {
		t.Fatalf("valid service pin did not reach selected board's replay preflight: %v %s", err, out)
	}
}
