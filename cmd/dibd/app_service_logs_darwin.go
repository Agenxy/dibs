// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"os"
	"path/filepath"
	"syscall"
)

// The embedded manifest cannot interpolate a user's board path. Preserve the
// legacy unit's log destination from inside the final, pinned service image.
func appServiceLogs(dir string) error {
	// #nosec G304 -- the selected local service board, not a participant path.
	f, err := os.OpenFile(filepath.Join(dir, "dibd.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Dup2(int(f.Fd()), 1); err != nil {
		return err
	}
	return syscall.Dup2(int(f.Fd()), 2)
}
