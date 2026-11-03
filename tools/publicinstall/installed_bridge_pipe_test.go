// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestInstalledBridgeDeadlineClosesItsManualStdoutReader(t *testing.T) {
	for _, protocol := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(protocol, func(t *testing.T) {
			r, _ := commandTestFixture(t, "dibs")
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			_ = listener.(*net.TCPListener).SetDeadline(time.Now().Add(5 * time.Second))
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			r.ctx = ctx
			done := make(chan error, 1)
			go func() {
				done <- r.installedBridge(r.root, r.root, []string{
					"DIBS_TEST_INSTALLED_MODE=retained-stdout",
					"DIBS_TEST_INSTALLED_CONTROL=" + listener.Addr().String(),
					"DIBS_TEST_INSTALLED_PROTOCOL=" + protocol,
				}, protocol, 0)
			}()
			controls := map[byte]net.Conn{}
			joined := false
			t.Cleanup(func() {
				cancel()
				for _, control := range controls {
					_ = control.SetDeadline(time.Now().Add(5 * time.Second))
					_, _ = control.Write([]byte{'x'})
					if extra, err := io.ReadAll(control); err != nil || len(extra) != 0 {
						t.Errorf("fixture exit receipt: %q %v", extra, err)
					}
					_ = control.Close()
				}
				if !joined {
					select {
					case err := <-done:
						if err == nil {
							t.Error("cancelled bridge cleanup qualified as success")
						}
					case <-time.After(5 * time.Second):
						t.Error("bridge did not finish after fixture release")
					}
				}
			})
			for range 2 {
				control, err := listener.Accept()
				if err != nil {
					t.Fatal("setup fixture did not connect:", err)
				}
				_ = control.SetReadDeadline(time.Now().Add(5 * time.Second))
				var role [1]byte
				if _, err := io.ReadFull(control, role[:]); err != nil || (role[0] != 'p' && role[0] != 'g') || controls[role[0]] != nil {
					_ = control.Close()
					t.Fatalf("setup fixture role: %q %v", role, err)
				}
				controls[role[0]] = control
			}
			parent := controls['p']
			if _, err := parent.Write([]byte{'x'}); err != nil {
				t.Fatal(err)
			}
			if extra, err := io.ReadAll(parent); err != nil || len(extra) != 0 {
				t.Fatalf("setup: direct child did not exit: %q %v", extra, err)
			}
			_ = parent.Close()
			delete(controls, 'p')
			if ctx.Err() != nil {
				t.Fatal("setup: fixture missed its probe deadline")
			}
			deadline, _ := ctx.Deadline()
			select {
			case err := <-done:
				joined = true
				if err == nil {
					t.Fatal("incomplete bridge handshake qualified as success")
				}
				if len(r.report.Steps) != 1 || r.report.Steps[0].Exit == 0 {
					t.Fatalf("bridge failure missing from its receipt: %+v", r.report.Steps)
				}
			case <-time.After(time.Until(deadline) + 3*time.Second):
				t.Fatal("installed bridge decoder outlived its context deadline and pipe-drain allowance")
			}
		})
	}
}
