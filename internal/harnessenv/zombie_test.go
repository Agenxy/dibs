// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package harnessenv

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/procexec"
	"github.com/agenxy/dibs/internal/procinfo"
	"github.com/agenxy/dibs/internal/testexec"
)

func TestZombieAfterSnapshotDoesNotStartPathFallback(t *testing.T) {
	body, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "ChatGPT")
	if err := os.WriteFile(binary, body, 0o700); err != nil {
		t.Fatal(err)
	}
	testexec.PrepareExecutable(t, exec.Command(binary, "-test.run=^$"))
	read, release, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd := procexec.Command(ctx, binary, "-test.run=^TestZombieSnapshotChild$")
	cmd.Env = append(os.Environ(), "DIBS_ZOMBIE_CHILD=1")
	cmd.Stdin = read
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal("setup child:", err)
	}
	_ = read.Close()
	t.Cleanup(func() { _ = release.Close(); cancel(); _ = cmd.Wait() })
	if ready, err := bufio.NewReader(output).ReadString('\n'); err != nil || ready != "ready\n" {
		t.Fatal("setup readiness:", ready, err)
	}
	pid := cmd.Process.Pid
	pass, err := readProcessPass(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	row := pass.rows[pid]
	if row.kernel.PID != pid || row.comm != "ChatGPT" {
		t.Fatalf("setup: real live app candidate missing: %+v", row)
	}
	if same, err := procinfo.SameProcess(row.kernel); err != nil || !same {
		t.Fatal("setup: live child identity did not match:", same, err)
	}
	if err := release.Close(); err != nil {
		t.Fatal(err)
	}
	testexec.AwaitZombie(t, pid) // no Wait until every assertion is finished
	probes := 0
	restore := procexec.ObserveCommandArgs(func(name string, args []string) {
		if filepath.Base(name) == "ps" && slices.Contains(args, strconv.Itoa(pid)) {
			probes++
		}
	})
	defer restore()
	if same, err := procinfo.SameProcess(row.kernel); err != nil || same {
		t.Errorf("cached live row still matched an exited child: same=%t err=%v", same, err)
	}
	if got := pass.path(pid); got.state != processAbsent {
		t.Errorf("exited child after snapshot was not absent: %+v", got)
	}
	observed, err := AppForBridges(t.Context(), []BridgeIncarnation{{PID: pid, Start: row.start}})
	if err != nil || len(observed) != 1 || observed[0].Present {
		t.Errorf("public bridge observation retained a zombie: %+v %v", observed, err)
	}
	_, _ = ChatGPTAppEpoch() // real idle app inventory, with a zombie app name
	if probes != 0 {
		t.Errorf("zombie observation started %d command fallbacks for pid %d", probes, pid)
	}
}

func TestZombieSnapshotChild(t *testing.T) {
	if os.Getenv("DIBS_ZOMBIE_CHILD") != "1" {
		return
	}
	_, _ = fmt.Fprintln(os.Stdout, "ready")
	_, _ = io.Copy(io.Discard, os.Stdin)
}
