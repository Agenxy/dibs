// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/paths"
)

func TestConfigureRefusedServiceOutlivesCLIAndMigratesBack(t *testing.T) {
	f := newAppServiceTestFixture(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIBS_TEST_APP_SERVICE_ADDR", addr)
	t.Setenv("DIBS_TEST_APP_SERVICE_DAEMON", "1")
	if err := os.WriteFile(filepath.Join(f.dir, "dibs.toml"), []byte("addr = '"+addr+"'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.state, []byte("enabled"), 0o600); err != nil {
		t.Fatal(err)
	}
	// This is the old private daemon, reaped promptly when configure stops it.
	old := exec.Command(f.root, "-dir", f.dir, "-addr", "http://"+addr)
	if err := old.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = old.Wait(); close(done) }()
	t.Cleanup(func() { _ = old.Process.Kill(); <-done })
	t.Cleanup(func() { _ = stopDaemon(f.dir) })
	if _, err := waitForBoard(addr, 5*time.Second, nil); err != nil {
		t.Fatal("old board setup:", err)
	}
	if live, err := paths.LiveDaemons(); err != nil || len(live) != 1 || live[0].PID != old.Process.Pid {
		t.Fatalf("old PID setup: %v %v", live, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	driver := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestConfigureRefusedServiceHandbackDriver$")
	driver.Env = append(os.Environ(), "DIBS_TEST_APP_SERVICE_HANDBACK_DRIVER=1")
	// Only this private process group is signalled. A detached recovery daemon
	// must not inherit the terminal group the configure command leaves behind.
	driver.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := driver.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "serving unsupervised") {
		t.Fatalf("configure refusal/recovery: %v %s", err, out)
	}
	live, err := paths.LiveDaemons()
	if err != nil || len(live) != 1 || live[0].PID == old.Process.Pid || live[0].PID == driver.Process.Pid {
		t.Fatalf("recovery did not leave its own live PID: %v %v", live, err)
	}
	recovered := live[0].PID
	if err := syscall.Kill(-driver.Process.Pid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
		t.Fatal(err)
	}
	if pgid, err := syscall.Getpgid(recovered); err != nil || pgid == driver.Process.Pid {
		t.Fatalf("recovery stayed in configure's process group: pgid=%d error=%v", pgid, err)
	}
	if board, err := fleetSnapshotAt(addr); err != nil || board.Serial != 7 {
		t.Fatalf("board did not outlive configure: %+v %v", board, err)
	}
	// The next real configure must stop that manual daemon before the native
	// fixture is allowed to stand in for the new enabled service.
	executor := executeAppServiceCommand
	t.Cleanup(func() { executeAppServiceCommand = executor })
	registrations := 0
	executeAppServiceCommand = func(cmd *exec.Cmd) ([]byte, error) {
		out, err := cmd.CombinedOutput()
		if err == nil && cmd.Args[1] == "--service-register" {
			registrations++
			if live, err := paths.LiveDaemons(); err != nil || len(live) != 0 {
				t.Fatalf("manual daemon not stopped before registration: %v %v", live, err)
			}
			err = startDaemon(f.root, f.dir, "", daemonState{addr: "http://" + addr})
		}
		return out, err
	}
	output := capturedUpgradeOutput(t, func() { err = serviceCommand(nil) })
	if err != nil || registrations != 1 || !strings.Contains(output, "upgraded:") {
		t.Fatalf("manual-to-service hand-back: %v registers=%d %s", err, registrations, output)
	}
	if live, err := paths.LiveDaemons(); err != nil || len(live) != 1 || live[0].PID == recovered {
		t.Fatalf("hand-back retained old manual PID: %v %v", live, err)
	}
}

func TestConfigureRefusedServiceHandbackDriver(t *testing.T) {
	if os.Getenv("DIBS_TEST_APP_SERVICE_HANDBACK_DRIVER") != "1" {
		return
	}
	readAppServiceJob = func() ([]byte, error) {
		trace, err := os.ReadFile(os.Getenv("DIBS_TEST_APP_SERVICE_TRACE"))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if !strings.Contains(string(trace), `"mode":"register"`) {
			return nil, errAppServiceNotLoaded
		}
		return launchOutcomeFixture("fixture", 1, "spawn failed", "OS_REASON_CODESIGNING", ""), nil
	}
	// No manual-start seam: this invokes the production detached start.
	if err := serviceCommand(nil); err == nil || !strings.Contains(err.Error(), "OS_REASON_CODESIGNING") {
		t.Fatalf("native refusal was hidden: %v", err)
	}
}
