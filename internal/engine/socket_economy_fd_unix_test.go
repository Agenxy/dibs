// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build unix

package engine

import (
	"errors"
	"net"
	"syscall"
	"testing"
	"time"
)

// Hold the same extra reference a child inherits between fork and exec. Dup
// preserves nonblocking mode; UnixListener.File would change the shared socket
// to blocking and could wedge the fixture's already-blocked Accept at Close.
func TestSocketEconomyReceiverCloseWaitsForOtherDescriptor(t *testing.T) {
	f := newDaemonEconomyFixture(t)
	raw, err := f.sock.(*net.UnixListener).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var fd int
	var dupErr error
	if err := raw.Control(func(original uintptr) {
		syscall.ForkLock.RLock()
		defer syscall.ForkLock.RUnlock()
		fd, dupErr = syscall.Dup(int(original))
		if dupErr == nil {
			syscall.CloseOnExec(fd)
		}
	}); err != nil || dupErr != nil {
		t.Fatalf("duplicate listener: %v %v", err, dupErr)
	}
	done := make(chan error, 1)
	go func() {
		defer func() { _ = syscall.Close(fd) }()
		for {
			syscall.ForkLock.RLock()
			conn, _, err := syscall.Accept(fd)
			if err == nil {
				syscall.CloseOnExec(conn)
			}
			syscall.ForkLock.RUnlock()
			if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) {
				select {
				case <-f.ctx.Done():
					done <- f.ctx.Err()
					return
				case <-time.After(time.Millisecond):
				}
				continue
			}
			if err != nil {
				done <- err
				return
			}
			if err := syscall.SetNonblock(conn, false); err != nil {
				_ = syscall.Close(conn)
				done <- err
				return
			}
			var b [1]byte
			_, err = syscall.Read(conn, b[:]) // the setup's client close releases this reference
			_ = syscall.Close(conn)
			done <- err
			return
		}
	}()
	f.closeReceiver(t)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
