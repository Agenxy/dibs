// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package procexec

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 4 && os.Args[1] == "--dibs-procexec-fixture" {
		os.Exit(pipeFixture(os.Args[2], os.Args[3]))
	}
	os.Exit(m.Run())
}

// The grandchild alone owns the control connection. EOF on it is the cleanup
// receipt: os.Exit closes it, so no orphan remains behind a passing test.
func pipeFixture(mode, address string) int {
	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		return 10
	}
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return 11
	}
	if mode == "grandchild" {
		st, err := os.Stdout.Stat()
		if err != nil || st.Mode()&os.ModeNamedPipe == 0 {
			return 12
		}
		if _, err := os.Stdout.WriteString("grandchild owns stdout\n"); err != nil {
			return 13
		}
		if _, err := conn.Write([]byte{'g'}); err != nil {
			return 15
		}
		var release [1]byte
		if _, err := io.ReadFull(conn, release[:]); err != nil || release[0] != 'x' {
			return 16
		}
		return 0
	}
	if mode != "exit" && mode != "cancel" {
		return 20
	}
	if _, err := conn.Write([]byte{'p'}); err != nil {
		return 21
	}
	child := exec.Command(os.Args[0], "--dibs-procexec-fixture", "grandchild", address) // #nosec G204 -- this test binary, private control channel
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if err := child.Start(); err != nil {
		return 22
	}
	if err := child.Process.Release(); err != nil {
		return 23
	}
	var release [1]byte
	if _, err := io.ReadFull(conn, release[:]); err != nil || mode != "exit" || release[0] != 'e' {
		return 24
	}
	return 0
}

func TestCommandSetsPipeDrainBound(t *testing.T) {
	cmd := Command(t.Context(), "unused-fixture", "literal space", "$(not-a-shell)")
	if cmd.WaitDelay != time.Second {
		t.Fatalf("WaitDelay = %s, want one second", cmd.WaitDelay)
	}
	if len(cmd.Args) != 3 || cmd.Args[1] != "literal space" || cmd.Args[2] != "$(not-a-shell)" {
		t.Fatalf("argv was changed: %q", cmd.Args)
	}
}

func TestWaitIsBoundedWhenGrandchildHoldsStdout(t *testing.T) {
	for _, mode := range []string{"exit", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = ln.Close() }()
			if err := ln.(*net.TCPListener).SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cmd := Command(ctx, os.Args[0], "--dibs-procexec-fixture", mode, ln.Addr().String()) // #nosec G204 -- this test binary
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			controls := map[byte]net.Conn{}
			waited := false
			t.Cleanup(func() {
				cancel()
				for role, control := range controls {
					_ = control.SetDeadline(time.Now().Add(5 * time.Second))
					if role == 'g' {
						if _, err := control.Write([]byte{'x'}); err != nil {
							t.Errorf("releasing grandchild: %v", err)
						}
					}
					if extra, err := io.ReadAll(control); err != nil || len(extra) != 0 {
						t.Errorf("%c exit receipt: %q %v", role, extra, err)
					}
					_ = control.Close()
				}
				if !waited {
					select {
					case err := <-done:
						t.Logf("fixture Wait after release: %v", err)
					case <-time.After(5 * time.Second):
						t.Error("fixture Wait did not finish after release")
					}
				}
			})
			for range 2 {
				control, err := ln.Accept()
				if err != nil {
					t.Fatalf("fixture did not connect: %v", err)
				}
				if err := control.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
					t.Fatal(err)
				}
				var role [1]byte
				if _, err := io.ReadFull(control, role[:]); err != nil || (role[0] != 'p' && role[0] != 'g') || controls[role[0]] != nil {
					_ = control.Close()
					t.Fatalf("fixture did not establish its role: %q %v", role, err)
				}
				controls[role[0]] = control
			}
			started := time.Now()
			if mode == "cancel" {
				select {
				case err := <-done:
					waited = true
					t.Fatalf("parent exited before cancellation: %v", err)
				default:
				}
				cancel()
			} else if _, err := controls['p'].Write([]byte{'e'}); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				waited = true
				if mode == "exit" && !errors.Is(err, exec.ErrWaitDelay) {
					t.Fatalf("successful child with retained stdout returned %v, want ErrWaitDelay", err)
				}
				if mode == "cancel" && (err == nil || ctx.Err() != context.Canceled) {
					t.Fatalf("cancelled child returned err=%v context=%v", err, ctx.Err())
				}
				if !bytes.Contains(output.Bytes(), []byte("grandchild owns stdout")) {
					t.Fatalf("fixture stdout was not captured: %q", output.Bytes())
				}
				t.Logf("%s Wait returned after %s: %v", mode, time.Since(started), err)
			case <-time.After(3 * time.Second):
				t.Fatal("Wait exceeded the one-second pipe drain bound plus two seconds scheduling allowance")
			}
		})
	}
}
