// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package localtransport

import (
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// Serial behavioral tests can hold the actual publication boundary while two
// real Listen calls prepare their sockets. Production always uses the native
// exclusive rename; no caller may replace a concurrent listener's endpoint.
var publishSocket = renameSocketExclusive

// Listen already validated this directory's UID, exact 0700 mode and ACL.
// Prepare inside that SAME private directory, then expose only the secured
// inode. An absent final name is safely retryable; a visible 0755 socket is an
// unsafe endpoint that a bridge must permanently refuse.
func listenPrepared(dir, path string) (net.Listener, error) {
	// Nine bytes, exactly SocketName's length: do not reduce the maximum
	// usable profile path on Darwin. This random name provides uniqueness,
	// not authority; the containing private directory provides that boundary.
	temporary := filepath.Join(dir, "."+rand.Text()[:8])
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: temporary, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("prepare local IPC %s: %w; "+
			"inspect the private profile and choose a shorter path if needed", path, err)
	}
	ln.SetUnlinkOnClose(false)
	info, err := os.Lstat(temporary)
	if err != nil {
		_ = ln.Close()
		return nil, socketError(path, err)
	}
	published := false
	defer func() {
		if !published {
			_ = ln.Close()
			removeSocketIfSame(temporary, info)
		}
	}()
	if err := os.Chmod(temporary, 0o600); err != nil {
		return nil, socketError(path, err)
	}
	if err := publishSocket(temporary, path); err != nil {
		return nil, fmt.Errorf("publish local IPC %s: %w; "+
			"inspect this profile's endpoint and filesystem before retrying startup; "+
			"never replace a serving daemon", path, err)
	}
	published = true
	return &listener{UnixListener: ln, path: path, inode: info}, nil
}

func removeSocketIfSame(path string, inode os.FileInfo) {
	if now, err := os.Lstat(path); err == nil && os.SameFile(inode, now) {
		_ = os.Remove(path)
	}
}
