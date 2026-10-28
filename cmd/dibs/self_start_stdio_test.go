// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/build"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/mcp"
	"github.com/agenxy/dibs/internal/testexec"
)

// Compile a counting observer into ONLY the private child, not the product or
// the parent daemon. Its armed receipt proves the actual bridge has the hook.
// The observer calls the real runner; a zero is not a substituted probe result.
func ownStartObservedBridge(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(wd, "..", ".."))
	path := filepath.Join(root, "internal", "harnessenv", "harnessenv.go")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source = append(source, []byte(`
func init() {
    path := os.Getenv("DIBS_TEST_OWN_START_PROBES")
    if path == "" { return }
    if err := os.WriteFile(path, []byte("armed\n"), 0600); err != nil { panic(err) }
    original := appProbeOutput
    appProbeOutput = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
        f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
        if err != nil { panic(err) }
        if _, err := f.WriteString("probe\n"); err != nil { panic(err) }
        if err := f.Close(); err != nil { panic(err) }
        return original(ctx, binary, args...)
    }
}
`)...)
	dir := t.TempDir()
	replacement := filepath.Join(dir, "harnessenv.go")
	if err := os.WriteFile(replacement, source, 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{"Replace": map[string]string{path: replacement}})
	if err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(dir, "overlay.json")
	if err := os.WriteFile(overlay, data, 0o600); err != nil {
		t.Fatal(err)
	}
	// Linux comm permits both spaces and ')'; exercise the actual native reader.
	child := filepath.Join(dir, "bridge ) observed")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "test", "-c", "-race", "-overlay="+overlay,
		"-ldflags=-X github.com/agenxy/dibs/internal/build.Version="+build.Version, "-o", child, "./cmd/dibs")
	command.Dir = root
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("setup: compile observed real bridge: %v\n%s", err, out)
	}
	testexec.PrepareExecutable(t, exec.Command(child, "--dibs-test-prepare"))
	return child
}

// Ancestry now primes one asynchronous inventory of its own. The native self
// start stamp must still avoid inventory: falling back there adds a SECOND
// probe. The observer calls the real OS runner for both paths.
func TestFirstBridgeRegisterOnlyInventoriesAncestry(t *testing.T) {
	child := ownStartObservedBridge(t)
	count := filepath.Join(t.TempDir(), "inventory-probes")
	// Arm only the real bridge, after its harmless preparation has exited.
	t.Setenv("DIBS_TEST_OWN_START_PROBES", count)
	type identity struct {
		pid   int
		start string
	}
	forwarded := make(chan identity, 1)
	wrap := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			var request struct {
				Params struct {
					Name string         `json:"name"`
					Meta map[string]any `json:"_meta"`
				} `json:"params"`
			}
			if json.Unmarshal(body, &request) == nil && request.Params.Name == "register" {
				pid, _ := request.Params.Meta[mcp.BridgePIDMetaKey].(float64)
				start, _ := request.Params.Meta[mcp.BridgeStartMetaKey].(string)
				if pid > 1 {
					select {
					case forwarded <- identity{pid: int(pid), start: start}:
					default:
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	}
	f := newEconomyFixtureAtExecutable(t, "71f97290-0001-4000-8000-111111111111", sockPath(t), wrap, child)
	select {
	case observed := <-forwarded:
		if observed.pid != f.diagnostics.cmd.Process.Pid || observed.start == "" {
			t.Fatalf("first register did not identify its actual bridge: %+v", observed)
		}
		// This OTHER-pid lookup deliberately retains the real ps inventory.
		if reference := harnessenv.ProcessStart(observed.pid); reference == "" || reference != observed.start {
			t.Fatalf("first register start=%q, want actual ps start=%q", observed.start, reference)
		}
	default:
		t.Fatal("first actual register was not observed with process metadata")
	}
	// The ancestry inventory is asynchronous. Count only after EOF has ended
	// the session and Wait has reaped the child, so a late probe cannot either
	// hide a native-start fallback or make a correct bridge fail spuriously.
	stdin, ok := f.in.(io.Closer)
	if !ok {
		t.Fatal("setup: actual bridge stdin cannot be closed")
	}
	if err := stdin.Close(); err != nil {
		t.Fatal("setup: close actual bridge stdin:", err)
	}
	select {
	case <-f.diagnostics.exited:
		if f.diagnostics.exitErr != nil {
			t.Fatalf("setup: actual bridge did not exit cleanly after EOF: %s", f.diagnostics.exitState())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("setup: actual bridge did not exit after EOF: %s", f.diagnostics.report(nil))
	}
	data, err := os.ReadFile(count)
	if err != nil {
		t.Fatalf("setup: actual bridge did not arm its inventory observer: %v", err)
	}
	if string(data) != "armed\nprobe\n" {
		t.Fatalf("first register must inventory ancestry once and obtain its own stamp natively: %q", data)
	}
}
