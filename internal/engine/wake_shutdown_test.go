// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/testcodexipc"
	"github.com/agenxy/dibs/internal/testexec"
)

func shutdownWakeEngine(t *testing.T, binary string) (*Engine, context.Context, context.CancelFunc, string) {
	t.Helper()
	e := New(core.NewState("wake-shutdown", core.DefaultLimits()), &memLedger{}, deadProber{})
	e.SetWakeCommands(map[string]WakeCommand{"codex": {Cooldown: time.Millisecond, Argv: []string{
		binary, "queue", "--thread", "{thread}", "--message", "{message}",
	}}})
	ctx, cancel := context.WithCancel(context.Background())
	go e.Run(ctx)
	t.Cleanup(cancel)
	contactReplayDo(t, e, ctx, &core.Op{
		Kind: core.OpRegister, Name: "worker", SessionID: testcodexipc.Thread,
		Agent: &core.AgentInfo{Harness: "Codex", Surface: harnessenv.ChatGPTApp},
	})
	sender := contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpRegister, Name: "sender"})["token"].(string)
	return e, ctx, cancel, sender
}

func TestShutdownJoinsWakeInsideAppCallback(t *testing.T) {
	for _, mode := range []string{"initial", "retry"} {
		t.Run(mode, func(t *testing.T) { testShutdownJoinsWakeInsideAppCallback(t, mode) })
	}
}

func TestShutdownAbandonsUnresponsiveAppCallback(t *testing.T) {
	testShutdownJoinsWakeInsideAppCallback(t, "abandon")
}

func testShutdownJoinsWakeInsideAppCallback(t *testing.T, mode string) {
	t.Helper()
	t.Setenv("DIBS_DIR", t.TempDir())
	testcodexipc.Start(t, "no-owner", nil)
	if mode == "retry" {
		t.Setenv("DIBS_TEST_QUEUE_FAIL_ONCE", filepath.Join(t.TempDir(), "failed-first"))
	}
	binary := filepath.Join(t.TempDir(), "codex")
	if out, err := exec.Command("go", "build", "-o", binary, "../wakeexec/testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("setup queue fixture: %v %s", err, out)
	}
	testexec.PrepareExecutable(t, exec.Command(binary, "--version"))
	entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	previous := shower
	shower = &harnessenv.Shower{Holds: func(string) bool {
		defer close(returned)
		close(entered)
		<-release
		return true
	}}
	defer func() { shower = previous }()
	e, ctx, cancel, sender := shutdownWakeEngine(t, binary)
	nativeAppSend(t, e, ctx, sender)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("setup: production wake did not enter app callback")
	}
	cancel()
	<-e.stopping // writer has actually left its loop; this is not scheduler timing
	if mode == "abandon" {
		started := time.Now()
		select {
		case <-e.Done():
			if elapsed := time.Since(started); elapsed < 9*time.Second {
				t.Errorf("shutdown abandoned a worker before its grace: %s", elapsed)
			}
		case <-time.After(12 * time.Second):
			t.Error("shutdown ignored its cap for an uncancellable app callback")
		}
		lateCtx, lateCancel := context.WithTimeout(context.Background(), time.Second)
		defer lateCancel()
		if _, err := e.Do(lateCtx, &core.Op{Kind: core.OpAckBoard, Token: sender}); err == nil {
			t.Error("stopped writer admitted a late operation after abandoning the worker")
		}
		once.Do(func() { close(release) })
		<-returned // release the deliberately stuck fixture before restoring it
		return
	}
	select {
	case <-e.Done():
		t.Error("Done closed while its delivery worker was still inside the app callback")
	case <-time.After(25 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	select {
	case <-e.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not finish after the app callback returned")
	}
}

func TestShutdownCancelsRunningQueueCommand(t *testing.T) {
	t.Setenv("DIBS_DIR", t.TempDir())
	testcodexipc.Start(t, "no-owner", nil)
	binary := filepath.Join(t.TempDir(), "codex")
	if out, err := exec.Command("go", "build", "-o", binary, "../wakeexec/testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("setup queue fixture: %v %s", err, out)
	}
	testexec.PrepareExecutable(t, exec.Command(binary, "--version"))
	marker := filepath.Join(t.TempDir(), "blocked-command")
	t.Setenv("DIBS_TEST_QUEUE_BLOCK", marker)
	e, ctx, cancel, sender := shutdownWakeEngine(t, binary)
	nativeAppSend(t, e, ctx, sender)
	var pid int
	until := time.Now().Add(5 * time.Second)
	for pid == 0 {
		if raw, err := os.ReadFile(marker); err == nil {
			pid, err = strconv.Atoi(string(raw))
			if err != nil || pid <= 0 {
				t.Fatalf("setup: invalid command pid %q: %v", raw, err)
			}
		}
		if time.Now().After(until) {
			t.Fatal("setup: queue executable did not enter its blocked operation")
		}
		if pid == 0 {
			<-time.After(5 * time.Millisecond)
		}
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Kill(); _ = process.Release() })
	cancel()
	select {
	case <-e.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not cancel the queue command's two-hour allowance")
	}
	if err := process.Signal(syscall.Signal(0)); err == nil {
		t.Error("Done returned while its blocked queue process was still alive")
	}
}

func TestShutdownCancelsUnresponsiveNativeApp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native app IPC uses a Unix socket")
	}
	t.Setenv("DIBS_DIR", t.TempDir())
	home, err := os.MkdirTemp("", "di-stop-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("CODEX_HOME", home)
	t.Setenv("DIBS_TEST_NATIVE_IPC", "1")
	dir := filepath.Join(home, "ipc")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ipc.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	accepted := make(chan net.Conn, 1)
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		accepted <- conn
		_, _ = io.Copy(io.Discard, conn) // app accepts bytes but NEVER answers
	}()
	e, ctx, cancel, sender := shutdownWakeEngine(t, filepath.Join(t.TempDir(), "codex"))
	nativeAppSend(t, e, ctx, sender)
	var conn net.Conn
	select {
	case conn = <-accepted:
		t.Cleanup(func() { _ = conn.Close(); <-closed })
	case <-time.After(5 * time.Second):
		t.Fatal("setup: native client never reached the unresponsive app")
	}
	cancel()
	select {
	case <-e.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled native call kept shutdown waiting for an app that never answers")
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Error("Done returned before the native call closed its connection")
	}
}
