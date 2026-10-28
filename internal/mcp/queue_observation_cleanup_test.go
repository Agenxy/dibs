// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/build"
	"github.com/agenxy/dibs/internal/paths"
	"github.com/agenxy/dibs/internal/testexec"
)

// Hold the last real app-memo writer after its ownership callback. Enter the
// original MCP/command contract: cleanup must wait for that write, rather than
// treating the callback as completion. Nothing contacts the person's app.
func TestQueueObservationCleanupWaitsForAppMemo(t *testing.T) {
	root := filepath.Join(repoRootForQueueCleanup(t), "internal", "mcp")
	source := filepath.Join(root, "queue_wake_observation_test.go")
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	observed := string(raw)
	const callback = "Holds: func(string) bool { contacts <- struct{}{}; return true },"
	if strings.Count(observed, callback) != 1 {
		t.Fatal("setup: original app-contact fixture was not found exactly once")
	}
	observed = strings.Replace(observed, "var opens atomic.Int64", "var opens atomic.Int64\n\tvar heldContacts atomic.Int64", 1)
	observed = strings.Replace(observed, callback, queueCleanupHeldContact, 1)
	observed = strings.Replace(observed, "previous := harnessenv.RealShower", `t.Cleanup(func() {
        if os.Getenv("DIBS_TEST_HELD_MEMO") != "" { t.Log("CONTROL_FIXTURE_CLEANUP") }
    })
    previous := harnessenv.RealShower`, 1)
	if !strings.Contains(observed, `"github.com/agenxy/dibs/internal/paths"`) {
		observed = strings.Replace(observed, `"github.com/agenxy/dibs/internal/testexec"`,
			"\"github.com/agenxy/dibs/internal/paths\"\n\t\"github.com/agenxy/dibs/internal/testexec\"", 1)
	}
	dir := t.TempDir()
	replacement := filepath.Join(dir, "observed_queue_test.go")
	if err := os.WriteFile(replacement, []byte(observed), 0o600); err != nil {
		t.Fatal(err)
	}
	overlay, err := json.Marshal(map[string]any{"Replace": map[string]string{source: replacement}})
	if err != nil {
		t.Fatal(err)
	}
	overlayPath := filepath.Join(dir, "overlay.json")
	if err := os.WriteFile(overlayPath, overlay, 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "observed-queue.test")
	buildCtx, stopBuild := context.WithTimeout(t.Context(), 90*time.Second)
	defer stopBuild()
	compile := exec.CommandContext(buildCtx, "go", "test", "-c", "-race", "-overlay="+overlayPath,
		"-ldflags=-X github.com/agenxy/dibs/internal/build.Version="+build.Version, "-o", binary, "./internal/mcp")
	compile.Dir = filepath.Dir(filepath.Dir(root))
	if out, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("setup: build observed MCP fixture: %v\n%s", err, out)
	}
	testexec.PrepareExecutable(t, exec.Command(binary, "-test.run=^$"))
	gatePath := filepath.Join(dir, "memo-writer.gate")
	gate, err := os.OpenFile(gatePath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			if err := gate.Close(); err != nil {
				t.Error("release held memo writer:", err)
			}
		})
	}
	t.Cleanup(release)
	if err := paths.LockExclusive(gate, false); err != nil {
		t.Fatal("setup: hold memo gate:", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	cmd := exec.CommandContext(ctx, binary,
		"-test.run=^TestQueueWakeObservationsThroughMCPAndActualCommand/fallback$", "-test.v")
	cmd.Env = append(os.Environ(), "DIBS_TEST_HELD_MEMO="+gatePath, "DIBS_NOTIFY=off")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	type outcome struct {
		err error
		out string
	}
	events, done := make(chan string, 4), make(chan outcome, 1)
	go func() {
		var output strings.Builder
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			output.WriteString(line + "\n")
			for _, event := range []string{"CONTROL_MEMO_WRITE_HELD", "CONTROL_FIXTURE_CLEANUP"} {
				if strings.Contains(line, event) {
					events <- event
				}
			}
		}
		waitErr := cmd.Wait()
		if scanErr := scanner.Err(); scanErr != nil {
			output.WriteString(scanErr.Error())
			if waitErr == nil {
				waitErr = scanErr
			}
		}
		done <- outcome{waitErr, output.String()}
	}()
	var result outcome
	waited := false
	t.Cleanup(func() {
		release()
		cancel()
		if !waited {
			result = <-done
		}
		if t.Failed() {
			t.Log(result.out)
		}
	})
	select {
	case event := <-events:
		if event != "CONTROL_MEMO_WRITE_HELD" {
			t.Fatal("setup: cleanup preceded the held writer:", event)
		}
	case result = <-done:
		waited = true
		t.Fatal("setup: fixture exited before the held memo write:", result.err)
	case <-time.After(5 * time.Second):
		t.Fatal("setup: actual second app contact did not reach the held writer")
	}
	select {
	case event := <-events:
		t.Fatal("fixture cleanup started while app-memo writer was held:", event)
	case result = <-done:
		waited = true
		t.Fatal("fixture exited while app-memo writer was held:", result.err)
	case <-time.After(2 * time.Second): // assert a blocked operation, never delay the repair
	}
	release()
	select {
	case result = <-done:
		waited = true
	case <-time.After(10 * time.Second):
		t.Fatal("fixture did not finish after the real memo writer was released")
	}
	if result.err != nil || !strings.Contains(result.out, "CONTROL_FIXTURE_CLEANUP") ||
		!strings.Contains(result.out, "--- PASS: TestQueueWakeObservationsThroughMCPAndActualCommand") {
		t.Fatalf("released fixture failed or skipped cleanup: %v\n%s", result.err, result.out)
	}
}

func repoRootForQueueCleanup(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(filepath.Join(wd, "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

const queueCleanupHeldContact = `Holds: func(string) bool {
    hold := os.Getenv("DIBS_TEST_HELD_MEMO") != "" && heldContacts.Add(1) == 2
    if hold { t.Log("CONTROL_MEMO_WRITE_HELD") }
    contacts <- struct{}{}
    if hold {
        f,err:=os.OpenFile(os.Getenv("DIBS_TEST_HELD_MEMO"),os.O_RDWR,0600)
        if err!=nil {panic(err)}
        if err=paths.LockExclusive(f,true); err!=nil {panic(err)}
        paths.Unlock(f)
        if err=f.Close(); err!=nil {panic(err)}
    }
    return true
},`
