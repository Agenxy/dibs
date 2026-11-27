// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build unix

package main

import (
	"os"
	"path/filepath"
	"syscall"
)

// lock blocks until no other k3gate run holds the seat. The kernel releases
// a flock when its holder exits, so a crashed run cannot wedge the next one.
func lock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	//nolint:gosec // G304: the lock path is fixed under the seat's own home
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() { _ = f.Close() }, nil
}
