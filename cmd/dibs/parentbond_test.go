package main

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A bridge must not be able to outlive the harness that spawned it.
//
// One bridge exists per harness session, so on a machine that opens and closes
// sessions all day, one that fails to exit is one orphan per session, each
// holding a subscription open against the daemon forever.
//
// EOF on stdin is the obvious signal and it is not enough. The bridge sees EOF
// only when the LAST holder of the pipe's write end closes it, and a harness
// that also spawns shells hands each one the same descriptors: a Claude Code
// killed while a Bash tool is running leaves the write end open, and the bridge
// waits on a pipe nobody will ever write to again.
//
// The arrangement below is that exactly: a real intermediate parent, a bridge
// beneath it, and a sibling holding the write end. Both halves matter and both
// were got wrong first. A version that gave the sibling the READ end passed
// against the unfixed tree, and so did one that killed the bridge itself rather
// than its parent. This one is checked against the commit before the fix, where
// it fails.
func TestTheBridgeCannotOutliveItsHarness(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns processes")
	}
	bridge := buildBridge(t)
	dir := dataDirWithSecret(t)

	// The test binary re-executed as the harness: a real parent process, with
	// no shell involved.
	harness := exec.Command(os.Args[0], "-test.run=TestHarnessHelper")
	harness.Env = append(os.Environ(),
		"DIBS_TEST_HARNESS=1",
		"DIBS_TEST_BRIDGE="+bridge,
		"DIBS_DIR="+dir,
	)
	stdout, err := harness.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = harness.Process.Kill(); _, _ = harness.Process.Wait() }()

	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("the harness never reported a bridge pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("bridge pid %q: %v", line, err)
	}
	// Settle, then insist it is genuinely running. An earlier version of this
	// test pointed the bridge at an empty data directory, so it exited at once
	// for want of a local secret and the test passed against every version,
	// including the one it was written to catch.
	time.Sleep(500 * time.Millisecond)
	if syscall.Kill(pid, 0) != nil {
		t.Fatal("the bridge exited before the harness was killed, so this test measures " +
			"nothing: check that the data directory it was given has a local.secret")
	}

	// SIGKILL: no clean shutdown, nothing closed on the way out. The sibling
	// still holds the write end, so stdin never closes.
	if err := harness.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_, _ = harness.Process.Wait()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return // gone: no orphan
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatal("the bridge outlived its harness. A sibling holding the pipe means stdin " +
		"never closes, so EOF cannot end the session and the process must be bound to " +
		"its parent instead")
}

// TestHarnessHelper is not a test: it is the harness process the test above
// kills. The standard re-exec idiom, so no shell is involved in building a
// process tree.
func TestHarnessHelper(t *testing.T) {
	if os.Getenv("DIBS_TEST_HARNESS") != "1" {
		t.Skip("helper: run only by TestTheBridgeCannotOutliveItsHarness")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	bridge := exec.Command(os.Getenv("DIBS_TEST_BRIDGE"), "mcp-stdio")
	bridge.Stdin = r
	if err := bridge.Start(); err != nil {
		t.Fatal(err)
	}
	// A stand-in for a Bash tool: it inherits the WRITE end and outlives this
	// process, so the bridge's stdin stays open after this process is killed.
	sibling := exec.Command("sleep", "30")
	sibling.Stdout = w
	if err := sibling.Start(); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	_ = w.Close() // only the sibling holds it now
	if _, err := os.Stdout.WriteString(strconv.Itoa(bridge.Process.Pid) + "\n"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Second) // killed long before this
}

// dataDirWithSecret is the least a bridge needs to start: it reads the local
// secret before it does anything else, and an empty directory makes it exit
// immediately for a reason that has nothing to do with what is being measured.
func dataDirWithSecret(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/local.secret", []byte("test-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// buildBridge compiles the CLI under test, because the guarantee is about a
// real process and its parent, which nothing in-process can stand in for.
func buildBridge(t *testing.T) string {
	t.Helper()
	bin := t.TempDir() + "/dibs"
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("building the bridge: %v: %s", err, out)
	}
	return bin
}

// Watching a pid that is not this process's parent answers immediately: a
// process that is already an orphan does not wait to be told.
func TestAProcessWithNoParentIsAlreadyGone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); watchParent(ctx, 1) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Error("watching a pid that is not this process's parent never returned")
	}
}
