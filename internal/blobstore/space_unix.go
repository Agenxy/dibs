// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build !windows

package blobstore

import (
	"errors"

	"golang.org/x/sys/unix"
)

// FreeBytes measures the same filesystem that receives ciphertext staging.
func (s *Store) FreeBytes() (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(s.blobsDir, &stat); err != nil {
		return 0, err
	}
	if stat.Bsize <= 0 {
		return 0, errors.New("invalid filesystem block size")
	}
	blockSize := uint64(stat.Bsize)
	if stat.Bavail > ^uint64(0)/blockSize {
		return 0, errors.New("filesystem size overflow")
	}
	return stat.Bavail * blockSize, nil
}
