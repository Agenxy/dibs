// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/procexec"
	"github.com/agenxy/dibs/internal/testexec"
)

// Enter the actual installed-client probe, not nativeReply alone. The external
// double exits while its descendant keeps stdout (and only stdout) open. The
// probe's own context must release its manual initialize and inventory reads.
func TestNativeInventoryReadStopsWithRetainedStdout(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "app-server-fixture")
	build := procexec.Command(t.Context(), "go", "build", "-o", bin, "./testdata/nativeinventorypipe")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("setup: app-server fixture build: %v %s", err, out)
	}
	testexec.PrepareExecutable(t, exec.Command(bin, "--prepare"))
	for _, phase := range []string{"initialize", "mcpServerStatus/list"} {
		t.Run(phase, func(t *testing.T) { nativeInventoryRetainedStdout(t, bin, phase) })
	}
}

func nativeInventoryRetainedStdout(t *testing.T, bin, phase string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	if err := ln.(*net.TCPListener).SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]string{"Address": ln.Addr().String(), "Phase": phase})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin+".json", raw, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := nativeInventory(ctx, t, "codex", bin, "", "https://[::1]/mcp", "fixture-only")
		done <- err
	}()
	controls := map[byte]net.Conn{}
	waited := false
	t.Cleanup(func() {
		cancel()
		for _, control := range controls {
			// EOF releases the private descendant, including on old-code RED.
			_ = control.Close()
		}
		if !waited {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("native inventory fixture did not finish after release")
			}
		}
	})
	for range 2 {
		control, err := ln.Accept()
		if err != nil {
			t.Fatal("setup: external app-server did not reach held phase:", err)
		}
		if err := control.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Fatal(err)
		}
		var role [1]byte
		_, err = io.ReadFull(control, role[:])
		if err != nil || (role[0] != 'p' && role[0] != 'g') || controls[role[0]] != nil {
			_ = control.Close()
			t.Fatalf("setup: app-server fixture role %q: %v", role, err)
		}
		controls[role[0]] = control
	}
	parent := controls['p']
	if _, err := parent.Write([]byte{'e'}); err != nil {
		t.Fatal(err)
	}
	if out, err := io.ReadAll(parent); err != nil || len(out) != 0 {
		t.Fatalf("setup: direct app-server did not exit: %q %v", out, err)
	}
	_ = parent.Close()
	delete(controls, 'p')
	cancel()
	select {
	case err := <-done:
		waited = true
		if err == nil {
			t.Fatal("incomplete native inventory was reported as success")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("native inventory manual read outlived cancellation with stdout retained")
	}
}
