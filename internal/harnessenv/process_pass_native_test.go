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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/procexec"
	"github.com/agenxy/dibs/internal/procinfo"
)

// A real executable in a private app-shaped fixture owns a real bridge child.
// The public decision reads the default native table and kernel paths; no fake
// typed table, configured flag or manually supplied path establishes the app.
func TestRealNativeBridgeAncestryFindsOwnerWithoutChildProbes(t *testing.T) {
	bridge, parent := nativeAppProcesses(t)
	var mu sync.Mutex
	commands := map[string]int{}
	restore := procexec.ObserveCommands(func(name string) { mu.Lock(); commands[name]++; mu.Unlock() })
	defer restore()
	observed, err := AppForBridges(t.Context(), []BridgeIncarnation{{PID: bridge.PID, Start: procinfo.Stamp(bridge.Start)}})
	if err != nil || len(observed) != 1 || !observed[0].Present || observed[0].App.PID != parent.PID || observed[0].App.Start != procinfo.Stamp(parent.Start) {
		t.Fatalf("real native ancestry did not find fixture owner: %+v %v", observed, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(commands) != 0 {
		t.Fatalf("real native ancestry prepared child probes: %v", commands)
	}
}

func TestNativePassMemoizesRelevantPathAndNeverReadsUnrelatedDenial(t *testing.T) {
	bridge, parent := nativeAppProcesses(t)
	rows, err := procinfo.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var unrelated procinfo.Process
	for _, row := range rows {
		if row.PID == os.Getpid() {
			unrelated = row
		}
	}
	if unrelated.PID == 0 {
		t.Fatal("setup: unrelated live process missing")
	}
	unrelated.Comm = "unrelated-protected-process"
	oldSnapshot, oldPath, oldFallback := nativeProcessSnapshot, nativeProcessPath, processPathFallback
	t.Cleanup(func() {
		nativeProcessSnapshot = oldSnapshot
		nativeProcessPath = oldPath
		processPathFallback = oldFallback
	})
	snapshots, reads, fallbacks := 0, map[int]int{}, 0
	nativeProcessSnapshot = func(context.Context) ([]procinfo.Process, error) {
		snapshots++
		return []procinfo.Process{bridge, parent, unrelated}, nil
	}
	nativeProcessPath = func(pid int) (string, error) {
		reads[pid]++
		if pid == unrelated.PID {
			return "", os.ErrPermission
		}
		return oldPath(pid)
	}
	processPathFallback = func(context.Context, int) (string, error) { fallbacks++; return "", os.ErrPermission }
	claims := make([]BridgeIncarnation, 128)
	for i := range claims {
		claims[i] = BridgeIncarnation{PID: bridge.PID, Start: procinfo.Stamp(bridge.Start)}
	}
	observed, err := AppForBridges(t.Context(), claims)
	if err != nil || len(observed) != len(claims) {
		t.Fatalf("native batch: %+v %v", observed, err)
	}
	for _, row := range observed {
		if !row.Present || row.App.PID != parent.PID {
			t.Fatal("shared native ancestor lost its owner")
		}
	}
	if snapshots != 1 || reads[parent.PID] != 1 || reads[unrelated.PID] != 0 || fallbacks != 0 {
		t.Fatalf("unrelated denial or repeated lazy read: snapshots=%d paths=%v fallbacks=%d", snapshots, reads, fallbacks)
	}
}

func TestMissingNativeAncestorIsUnknownWhenMetadataIsDenied(t *testing.T) {
	bridge, parent := nativeAppProcesses(t)
	oldSnapshot, oldStart, oldFallback := nativeProcessSnapshot, nativeProcessStart, processPathFallback
	t.Cleanup(func() {
		nativeProcessSnapshot = oldSnapshot
		nativeProcessStart = oldStart
		processPathFallback = oldFallback
	})
	nativeProcessSnapshot = func(context.Context) ([]procinfo.Process, error) { return []procinfo.Process{bridge}, nil }
	starts, fallbacks := 0, 0
	nativeProcessStart = func(pid int) (time.Time, error) {
		if pid != parent.PID {
			t.Fatalf("metadata lookup escaped requested ancestry: %d", pid)
		}
		starts++
		return time.Time{}, os.ErrPermission
	}
	processPathFallback = func(context.Context, int) (string, error) { fallbacks++; return "", os.ErrPermission }
	if _, err := AppForBridges(t.Context(), []BridgeIncarnation{{PID: bridge.PID, Start: procinfo.Stamp(bridge.Start)}}); err == nil {
		t.Fatal("a hidden parent became a known absent app")
	}
	if starts != 1 || fallbacks != 0 {
		t.Fatalf("unknown metadata caused a path or whole-table retry: starts=%d fallbacks=%d", starts, fallbacks)
	}
}

func TestKnownAbsentNativePathNeverFallsBack(t *testing.T) {
	bridge, parent := nativeAppProcesses(t)
	oldSnapshot, oldPath, oldFallback := nativeProcessSnapshot, nativeProcessPath, processPathFallback
	t.Cleanup(func() {
		nativeProcessSnapshot = oldSnapshot
		nativeProcessPath = oldPath
		processPathFallback = oldFallback
	})
	nativeProcessSnapshot = func(context.Context) ([]procinfo.Process, error) { return []procinfo.Process{bridge, parent}, nil }
	reads, fallbacks := 0, 0
	nativeProcessPath = func(pid int) (string, error) {
		if pid != parent.PID {
			t.Fatalf("unexpected path %d", pid)
		}
		reads++
		return "", os.ErrNotExist
	}
	processPathFallback = func(context.Context, int) (string, error) { fallbacks++; return "", os.ErrPermission }
	observed, err := AppForBridges(t.Context(), []BridgeIncarnation{{PID: bridge.PID, Start: procinfo.Stamp(bridge.Start)}})
	if err != nil || len(observed) != 1 || !observed[0].Present || observed[0].App.PID != 0 {
		t.Fatalf("known absent executable became an unknown/owning app: %+v %v", observed, err)
	}
	if reads != 1 || fallbacks != 0 {
		t.Fatalf("known absence triggered fallback: reads=%d fallbacks=%d", reads, fallbacks)
	}
}

func TestDeniedNativeAncestorStaysUnknownAndIsRetriedOnlyNextPass(t *testing.T) {
	bridge, parent := nativeAppProcesses(t)
	oldSnapshot, oldPath, oldFallback := nativeProcessSnapshot, nativeProcessPath, processPathFallback
	t.Cleanup(func() {
		nativeProcessSnapshot = oldSnapshot
		nativeProcessPath = oldPath
		processPathFallback = oldFallback
	})
	reads, fallbacks := 0, 0
	nativeProcessSnapshot = func(context.Context) ([]procinfo.Process, error) { return []procinfo.Process{bridge, parent}, nil }
	nativeProcessPath = func(pid int) (string, error) {
		if pid != parent.PID {
			t.Fatalf("unexpected native path %d", pid)
		}
		reads++
		return "", os.ErrPermission
	}
	processPathFallback = func(_ context.Context, pid int) (string, error) {
		if pid != parent.PID {
			t.Fatalf("fallback escaped the denied candidate: %d", pid)
		}
		fallbacks++
		return "", os.ErrPermission
	}
	if _, err := AppForBridges(t.Context(), []BridgeIncarnation{{PID: bridge.PID, Start: procinfo.Stamp(bridge.Start)}}); err == nil {
		t.Fatal("denied ancestry became a known absent app")
	}
	if reads != 1 || fallbacks != 1 {
		t.Fatalf("denied candidate was not one bounded fallback: %d %d", reads, fallbacks)
	}
	p, err := readProcessPass(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if got := p.path(parent.PID); got.state != processUnknown || got.err == nil {
			t.Fatalf("denied lazy path=%+v", got)
		}
	}
	if reads != 2 || fallbacks != 2 {
		t.Fatalf("unknown path was not memoized inside its pass: %d %d", reads, fallbacks)
	}
	if _, err := AppForBridges(t.Context(), []BridgeIncarnation{{PID: bridge.PID, Start: procinfo.Stamp(bridge.Start)}}); err == nil {
		t.Fatal("new pass silently promoted a denied path")
	}
	if reads != 3 || fallbacks != 3 {
		t.Fatalf("negative evidence survived into the next pass: %d %d", reads, fallbacks)
	}
}

func TestNativeCandidateNameIsNotExecutablePathAuthority(t *testing.T) {
	rows, err := procinfo.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var own procinfo.Process
	for _, row := range rows {
		if row.PID == os.Getpid() {
			own = row
		}
	}
	if own.PID == 0 {
		t.Fatal("setup: own kernel row missing")
	}
	own.Comm = "ChatGPT"
	oldSnapshot, oldPath := nativeProcessSnapshot, nativeProcessPath
	t.Cleanup(func() { nativeProcessSnapshot = oldSnapshot; nativeProcessPath = oldPath })
	reads := 0
	nativeProcessSnapshot = func(context.Context) ([]procinfo.Process, error) { return []procinfo.Process{own}, nil }
	nativeProcessPath = func(pid int) (string, error) { reads++; return oldPath(pid) }
	if epoch, known := ChatGPTAppEpoch(); !known || epoch != "absent" || reads != 1 {
		t.Fatalf("comm acquired app authority or bypassed native path: %q %t reads=%d", epoch, known, reads)
	}
}

func TestNativeInventoryErrorStaysUnknownWithoutTableFallback(t *testing.T) {
	oldSnapshot, oldOutput := nativeProcessSnapshot, appProbeOutput
	t.Cleanup(func() { nativeProcessSnapshot = oldSnapshot; appProbeOutput = oldOutput })
	snapshots, commands := 0, 0
	nativeProcessSnapshot = func(context.Context) ([]procinfo.Process, error) { snapshots++; return nil, os.ErrPermission }
	appProbeOutput = func(context.Context, string, ...string) ([]byte, error) { commands++; return nil, os.ErrPermission }
	if _, err := AppForBridges(t.Context(), []BridgeIncarnation{{PID: os.Getpid(), Start: "requested incarnation"}}); err == nil {
		t.Fatal("denied native inventory became known evidence")
	}
	if snapshots != 1 || commands != 0 {
		t.Fatalf("unknown whole inventory triggered a command retry: snapshots=%d commands=%d", snapshots, commands)
	}
}

func nativeAppProcesses(t *testing.T) (procinfo.Process, procinfo.Process) {
	t.Helper()
	return nativeAppProcessesWithArgs(t, "ChatGPT", nil)
}

func nativeAppProcessesWithArgs(t *testing.T, name string, extra []string) (procinfo.Process, procinfo.Process) {
	t.Helper()
	executable := filepath.Join(t.TempDir(), "ChatGPT.app", "Contents", "MacOS", name)
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, body, 0o700); err != nil {
		t.Fatal(err)
	}
	input, release, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	args := append([]string{"-test.run=^TestNativeAppProcessHelper$"}, extra...)
	parent := procexec.Command(ctx, executable, args...)
	parent.Env = append(os.Environ(), "DIBS_NATIVE_APP_ROLE=parent")
	parent.Stdin = input
	output, err := parent.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.Start(); err != nil {
		cancel()
		_ = input.Close()
		_ = release.Close()
		t.Fatal("setup app process:", err)
	}
	_ = input.Close()
	t.Cleanup(func() { _ = release.Close(); cancel(); _ = parent.Wait() })
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(output).ReadString('\n'); ready <- line }()
	var bridgePID int
	select {
	case line := <-ready:
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "ready" {
			t.Fatalf("setup app child readiness: %q", line)
		}
		bridgePID, err = strconv.Atoi(fields[1])
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("setup app child did not become ready")
	}
	rows, err := procinfo.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var bridgeRow, parentRow procinfo.Process
	for _, row := range rows {
		if row.PID == bridgePID {
			bridgeRow = row
		}
		if row.PID == parent.Process.Pid {
			parentRow = row
		}
	}
	if bridgeRow.PID == 0 || parentRow.PID == 0 || bridgeRow.PPID != parentRow.PID {
		t.Fatal("setup actual parent/child relation missing")
	}
	t.Cleanup(func() {
		if current, err := procinfo.Start(bridgePID); err == nil && current.Equal(bridgeRow.Start) {
			if p, err := os.FindProcess(bridgePID); err == nil {
				_ = p.Kill()
			}
		}
	})
	return bridgeRow, parentRow
}

func TestNativeAppProcessHelper(t *testing.T) {
	switch os.Getenv("DIBS_NATIVE_APP_ROLE") {
	case "parent":
		child := procexec.Command(context.Background(), os.Args[0], "-test.run=^TestNativeAppProcessHelper$")
		child.Env = append(os.Environ(), "DIBS_NATIVE_APP_ROLE=child")
		child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := child.Run(); err != nil {
			t.Fatal(err)
		}
	case "child":
		if _, err := io.WriteString(os.Stdout, fmt.Sprintf("ready %d\n", os.Getpid())); err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, os.Stdin)
	}
}

func TestDetectHiddenNativeAncestorStaysUnknown(t *testing.T) {
	bridge, parent := nativeAppProcesses(t)
	oldSnapshot, oldStart := nativeProcessSnapshot, nativeProcessStart
	t.Cleanup(func() { nativeProcessSnapshot = oldSnapshot; nativeProcessStart = oldStart })
	nativeProcessSnapshot = func(context.Context) ([]procinfo.Process, error) { return []procinfo.Process{bridge}, nil }
	starts := 0
	nativeProcessStart = func(pid int) (time.Time, error) {
		if pid != parent.PID {
			t.Fatalf("metadata lookup escaped ancestry: %d", pid)
		}
		starts++
		return time.Time{}, os.ErrPermission
	}
	if surface, err := DetectContext(t.Context(), bridge.PID); err == nil {
		t.Fatalf("Detect classified hidden native ancestry as known: surface=%q metadata_reads=%d", surface, starts)
	}
	if starts != 1 {
		t.Fatalf("requested hidden ancestor was not probed once: %d", starts)
	}
}
