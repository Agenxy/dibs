// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package localtransport

import (
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// ReadPrivateFile validates and reads the same no-follow descriptor. Configured
// TLS keys can live outside the profile; a pathname check followed by ReadFile
// there would reopen an object another writer might have replaced.
func ReadPrivateFile(path string) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if err := privateFileInfo(path, info); err != nil {
		return nil, err
	}
	if err := checkCredentialDescriptorACL(f); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}
