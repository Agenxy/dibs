// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package codexipc

import (
	"errors"
	"os"
	"path/filepath"
)

// Names enter private input only through the app's owner-only socket. Reject
// symlinks, another user's files and group/other access before connecting.
func privateEndpoint(path string) error {
	if os.Getenv("DIBS_TEST_FORBID_APP_OPEN") == "1" && os.Getenv("DIBS_TEST_NATIVE_IPC") != "1" {
		return ErrNoOwner // isolated daemon fixtures represent an absent desktop app
	}
	// #nosec G703 -- operator CODEX_HOME plus fixed ipc/ipc.sock, never a participant path.
	socket, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ErrNoOwner
	}
	if err != nil {
		return err
	}
	// #nosec G703 -- fixed parent of the same local app endpoint; ownership is checked below.
	dir, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return err
	}
	if socket.Mode()&os.ModeSocket == 0 || !dir.IsDir() ||
		socket.Mode().Perm()&0o077 != 0 || dir.Mode().Perm()&0o077 != 0 ||
		!ownedByUser(socket) || !ownedByUser(dir) {
		return errors.New("native app IPC endpoint is not an owner-only socket in a private directory")
	}
	return nil
}
