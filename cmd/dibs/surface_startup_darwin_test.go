// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/mcp"
	"github.com/agenxy/dibs/internal/paths"
	"github.com/agenxy/dibs/internal/testexec"
)

// Hold ancestry at the actual bridge lookup boundary, not the classifier or
// an engine setter. Registration must answer while it is held. Late evidence
// repair is tested separately from the reply bound.
func TestRegisterRepliesWhileAncestryIsHeld(t *testing.T) {
	surfaceStartupContract(t, true, false)
}

// Also used with an independently built old bridge to measure compatibility
// across real stdio and HTTP, rather than assuming an unchanged schema works.
func TestRegisterAcrossBridgeVersions(t *testing.T) {
	surfaceStartupContract(t, false, false)
}

func TestLateAncestryRepairsSurfaceWithoutAnotherModelCall(t *testing.T) {
	surfaceStartupContract(t, true, true)
}

// The first lookup completes with failure, rather than merely arriving late.
// No second model call can rescue the completion worker.
func TestFailedAncestryRepairsSurfaceWithoutAnotherModelCall(t *testing.T) {
	t.Setenv("DIBS_TEST_SURFACE_FAIL_ONCE", "1")
	surfaceStartupContract(t, true, true)
}

func TestFailedAncestryRepairsSurfaceWithAnInheritedGate(t *testing.T) {
	t.Setenv("DIBS_TEST_SURFACE_FAIL_ONCE", "1")
	surfaceStartupContractWithGateCopy(t, true, true, true)
}

func surfaceStartupContract(t *testing.T, held, repair bool) {
	t.Helper()
	surfaceStartupContractWithGateCopy(t, held, repair, false)
}

func surfaceStartupContractWithGateCopy(t *testing.T, held, repair, retainGate bool) {
	t.Helper()
	dir := cliIPCProfile(t)
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("DIBS_HOST_ID", "startup")
	t.Setenv("DIBS_NOTIFY", "off")
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), 0o600); err != nil {
		t.Fatal(err)
	}
	eng := startupEngine(t, dir)
	evidence := newAncestryEvidence(t)
	// The app-shaped executable supplies actual ps evidence to both bridge and
	// daemon while never contacting the operator's app.
	app := filepath.Join(t.TempDir(), "ChatGPT.app", "Contents", "MacOS", "ChatGPT")
	if err := os.MkdirAll(filepath.Dir(app), 0o700); err != nil {
		t.Fatal(err)
	}
	self, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(app, self, 0o700); err != nil {
		t.Fatal(err)
	}
	testexec.PrepareExecutable(t, exec.Command(app, "--dibs-test-prepare"))
	gate := filepath.Join(t.TempDir(), "surface-gate")
	f, err := os.OpenFile(gate, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = paths.UnlockAndClose(f) }()
	if err := paths.LockExclusive(f, false); err != nil {
		t.Fatal(err)
	}
	if retainGate {
		// A fork retains this open-file description until exec closes it.
		// Keep a copy across release to prove Close is not our release signal.
		duplicate, err := syscall.Dup(int(f.Fd()))
		if err != nil {
			t.Fatal(err)
		}
		syscall.CloseOnExec(duplicate)
		defer func() { _ = syscall.Close(duplicate) }()
	}
	srv := newLocalTestServer(t, dir, evidence.handler(mcp.New(eng)))
	defer srv.Close()
	cmd := exec.Command(app, "-test.run=^TestReconnectAppFixtureHelper$")
	bridgeBinary := os.Args[0]
	if binary := os.Getenv("DIBS_TEST_SURFACE_BRIDGE_BINARY"); binary != "" {
		bridgeBinary = binary
	}
	cmd.Env = append(os.Environ(), "DIBS_TEST_RECONNECT_APP=1", "DIBS_TEST_BRIDGE_BINARY="+bridgeBinary,
		"DIBS_ADDR="+srv.URL, "GOTRACEBACK=all")
	if held {
		cmd.Env = append(cmd.Env, "DIBS_TEST_SURFACE_GATE="+gate)
	}
	cmd.Stderr = evidence.stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	evidence.stages.note(ancestryAppStarting)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	evidence.stages.note(ancestryAppStarted)
	defer func() {
		evidence.reportFailure(t, cmd.Process)
		_ = in.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	request := map[string]any{
		"bridge": 0,
		"rpc": map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{
				"name": "register", "arguments": map[string]any{
					"name": "late-surface", "description": "bounded ancestry fixture", "nonce": "late-surface-nonce",
					"harness": "Codex", "session_id": "late-surface-session",
				},
			},
		},
	}
	start := time.Now()
	evidence.stages.note(ancestryRequestWriteStarting)
	if err := json.NewEncoder(in).Encode(request); err != nil {
		t.Fatal(err)
	}
	evidence.stages.note(ancestryRequestWritten)
	replies := make(chan []byte, 1)
	go func() { line, _ := bufio.NewReader(out).ReadBytes('\n'); replies <- line }()
	select {
	case line := <-replies:
		evidence.stages.note(ancestryReplyRead)
		result := economyToolResult(t, line)
		if result["token"] == nil {
			t.Fatalf("registration setup failed: %v", result)
		}
		t.Logf("register replied in %s (ancestry held=%v)", time.Since(start), held)
		if os.Getenv("DIBS_TEST_SURFACE_FAIL_ONCE") == "1" {
			// The child may inspect its worker only after the parent has read
			// and validated the real reply, outside the measured request path.
			if err := os.WriteFile(gate+".replied", []byte("replied"), 0o600); err != nil {
				t.Fatal("surface reply marker:", err)
			}
		}
	case <-time.After(5 * time.Second):
		if _, err := os.Stat(gate + ".entered"); err != nil {
			t.Fatalf("probe setup never entered held ancestry: %v", err)
		}
		t.Fatal("register did not reply within 5s while ancestry was held")
	}
	if !held {
		return
	}
	if _, err := os.Stat(gate + ".entered"); err != nil {
		t.Fatalf("probe setup never entered held ancestry: %v", err)
	}
	if got := startupSurface(t, eng); got != "" {
		t.Fatalf("pending ancestry was misclassified as %q", got)
	}
	if os.Getenv("DIBS_TEST_SURFACE_FAIL_ONCE") == "1" {
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(gate + ".waiting"); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("setup: actual surface completion worker never waited on the held lookup")
			}
			time.Sleep(time.Millisecond)
		}
		evidence.assertWaiterScansAfterReply(t)
	}
	if err := paths.UnlockAndClose(f); err != nil {
		t.Fatal(err)
	}
	if repair {
		deadline := time.Now().Add(5 * time.Second)
		for startupSurface(t, eng) != harnessenv.ChatGPTApp {
			if time.Now().After(deadline) {
				if os.Getenv("DIBS_TEST_SURFACE_FAIL_ONCE") == "1" {
					if _, err := os.Stat(gate + ".failed"); err != nil {
						t.Fatalf("setup: actual ancestry failure did not occur: %v", err)
					}
				}
				t.Fatal("late ancestry never corrected agent surface without another model request")
			}
			time.Sleep(5 * time.Millisecond)
		}
		if os.Getenv("DIBS_TEST_SURFACE_FAIL_ONCE") == "1" {
			if _, err := os.Stat(gate + ".failed"); err != nil {
				t.Fatalf("setup: actual ancestry failure did not occur: %v", err)
			}
		}
	}
}

func startupSurface(t *testing.T, eng *engine.Engine) string {
	t.Helper()
	board, err := eng.Board(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range board["agents"].([]map[string]any) {
		if row["id"] == "late-surface" {
			if info, _ := row["agent"].(*core.AgentInfo); info != nil {
				return info.Surface
			}
			return ""
		}
	}
	t.Fatal("setup: registered agent is missing")
	return ""
}
