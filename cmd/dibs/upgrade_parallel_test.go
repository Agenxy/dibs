// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/paths"
	"github.com/agenxy/dibs/internal/procexec"
	"github.com/agenxy/dibs/internal/testexec"
)

// The registry's liveness is a held file lock, not kill(pid,0). These rows
// therefore use distinct synthetic PID metadata while this private fixture
// owns the real locks. No test signals either PID or reads the user's registry.
func parallelPolicyRegistry(t *testing.T, mineParallel bool, otherParallel *bool) (string, paths.Daemon) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	t.Setenv("DIBS_DIR", dir)
	claim := func(row paths.Daemon) {
		t.Helper()
		release, err := paths.Claim(row, true)
		if err != nil {
			t.Fatal("setup: private registry claim:", err)
		}
		t.Cleanup(release)
	}
	claim(paths.Daemon{PID: os.Getpid(), Dir: dir, Addr: defaultAddr, Parallel: mineParallel})
	var other paths.Daemon
	if otherParallel != nil {
		other = paths.Daemon{PID: os.Getpid() + 1, Dir: t.TempDir(), Addr: "127.0.0.1:49997", Parallel: *otherParallel}
		claim(other)
	}
	live, err := paths.LiveDaemons()
	want := 1
	if otherParallel != nil {
		want++
	}
	if err != nil || len(live) != want {
		t.Fatalf("setup: private live registry: %v %v", live, err)
	}
	return dir, other
}

var parallelPolicyCases = [...]struct {
	name          string
	mineParallel  bool
	withOther     bool
	otherParallel bool
}{
	{"production-beside-parallel-fixture", false, true, true},
	{"production-alone", false, false, false},
	{"parallel-alone", true, false, false},
	{"parallel-beside-fixture", true, true, true},
	{"parallel-beside-nonparallel-board", true, true, false},
}

func TestAppMigrationUsesTheSelectedDaemonsOwnParallelPolicy(t *testing.T) {
	for _, tc := range parallelPolicyCases {
		t.Run(tc.name, func(t *testing.T) {
			var other *bool
			if tc.withOther {
				value := tc.otherParallel
				other = &value
			}
			dir, _ := parallelPolicyRegistry(t, tc.mineParallel, other)
			was := runningDaemon(dir) // production registry reader, no bit setter
			if was.pid != os.Getpid() || was.addr == "" || was.unknown {
				t.Fatalf("setup: selected live daemon was not read: %+v", was)
			}
			p := &plan{dir: dir, app: &appServicePlan{}, running: was}
			err := p.appStartupMatches()
			if tc.mineParallel {
				if err == nil || !strings.Contains(err.Error(), "permits parallel boards") {
					t.Errorf("selected daemon's explicit parallel policy was not refused: %v", err)
				}
			} else if err != nil {
				t.Errorf("another board's parallel flag blocked app migration: %v", err)
			}
		})
	}
}

func restartPolicyRecorder(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "restart-recorder")
	build := procexec.Command(t.Context(), "go", "build", "-o", bin, "./testdata/restartpolicy")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("setup: restart recorder build: %v %s", err, out)
	}
	testexec.PrepareExecutable(t, exec.Command(bin, "--prepare"))
	return bin
}

func TestUpgradeRestartPreservesTheSelectedDaemonsParallelFlag(t *testing.T) {
	bin := restartPolicyRecorder(t)
	for _, tc := range parallelPolicyCases {
		t.Run(tc.name, func(t *testing.T) {
			var other *bool
			if tc.withOther {
				value := tc.otherParallel
				other = &value
			}
			dir, _ := parallelPolicyRegistry(t, tc.mineParallel, other)
			record := filepath.Join(t.TempDir(), "argv.json")
			t.Setenv("DIBS_TEST_RESTART_ARGV", record)
			if err := startDaemon(bin, dir, "", runningDaemon(dir)); err != nil {
				t.Fatal("actual restart entry:", err)
			}
			raw, err := os.ReadFile(record)
			var argv []string
			if err != nil || json.Unmarshal(raw, &argv) != nil || addrFlag(argv) == "" {
				t.Fatalf("setup: actual exec did not record restart argv: %q %v", raw, err)
			}
			if got := slices.Contains(argv, "-allow-parallel"); got != tc.mineParallel {
				t.Errorf("restart changed selected daemon policy: argv=%v, own Parallel=%t", argv, tc.mineParallel)
			}
		})
	}
}

func TestUpgradeWithoutSelectedDaemonDoesNotBorrowFixturePolicy(t *testing.T) {
	otherParallel := true
	dir, _ := parallelPolicyRegistry(t, false, &otherParallel)
	if err := os.Remove(filepath.Join(paths.RunRegistryDir(), strconv.Itoa(os.Getpid())+".json")); err != nil {
		t.Fatal(err)
	}
	was := runningDaemon(dir)
	if was.pid != 0 || was.addr != "" || was.unknown {
		t.Fatalf("setup: selected daemon should be absent: %+v", was)
	}
	record := filepath.Join(t.TempDir(), "argv.json")
	t.Setenv("DIBS_TEST_RESTART_ARGV", record)
	if err := startDaemon(restartPolicyRecorder(t), dir, "", was); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(record)
	var argv []string
	if err != nil || json.Unmarshal(raw, &argv) != nil || len(argv) < 2 {
		t.Fatalf("setup: restart argv missing: %q %v", raw, err)
	}
	if slices.Contains(argv, "-allow-parallel") {
		t.Fatalf("absent selected daemon borrowed a fixture's policy: %v", argv)
	}
}

func TestUpgradeRefusesUnknownRegistryStrangerBeforeStopping(t *testing.T) {
	otherParallel := false
	dir, other := parallelPolicyRegistry(t, false, &otherParallel)
	path := filepath.Join(paths.RunRegistryDir(), strconv.Itoa(other.PID)+".json")
	if err := os.WriteFile(path, []byte("broken private fixture registration"), 0o600); err != nil {
		t.Fatal(err)
	}
	stopped := 0
	p := &plan{dir: dir, running: runningDaemon(dir), stop: func(string) error { stopped++; return nil }}
	err := p.cutover()
	for _, want := range []string{paths.RunRegistryDir(), "unreadable", "nothing was stopped", "dibs doctor"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("unknown registry row lacks diagnosis/corrective %q: %v", want, err)
		}
	}
	if stopped != 0 {
		t.Errorf("unattributed stranger still stopped the selected daemon %d times", stopped)
	}
	if err != nil && strings.Contains(err.Error(), "pid 0") {
		t.Error("unattributed registry row was assigned an invented PID")
	}
}

func TestUpgradeRefusesNonparallelStrangerBeforeStopping(t *testing.T) {
	otherParallel := false
	dir, other := parallelPolicyRegistry(t, false, &otherParallel)
	record := filepath.Join(t.TempDir(), "argv.json")
	t.Setenv("DIBS_TEST_RESTART_ARGV", record)
	stopped := 0
	p := &plan{
		dir: dir, installed: restartPolicyRecorder(t), running: runningDaemon(dir),
		stop: func(string) error { stopped++; return nil },
		// Start is NOT substituted: any missed refusal reaches the real exec.
		confirm: func(string) error { return nil },
	}
	for _, phase := range []struct {
		name string
		run  func() error
	}{
		{"preflight", p.preflight},
		{"cutover", p.cutover},
		{"direct-restart", func() error { return startDaemon(p.installed, dir, "", p.running) }},
	} {
		err := phase.run()
		for _, want := range []string{strconv.Itoa(other.PID), other.Dir, other.Addr, "nothing was stopped", "dibs stop", "-allow-parallel"} {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%s lacks other-board identity or corrective %q: %v", phase.name, want, err)
			}
		}
	}
	if stopped != 0 {
		t.Errorf("policy conflict stopped the selected board %d times", stopped)
	}
	if raw, err := os.ReadFile(record); !os.IsNotExist(err) {
		t.Errorf("policy conflict still executed restart: argv=%s, read=%v", raw, err)
	}
	if p.running.parallel {
		t.Error("non-parallel selected daemon was silently assigned -allow-parallel")
	}
}
