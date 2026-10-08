// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package blobstore

import "golang.org/x/sys/windows"

// FreeBytes measures the same filesystem that receives ciphertext staging.
func (s *Store) FreeBytes() (uint64, error) {
	path, err := windows.UTF16PtrFromString(s.blobsDir)
	if err != nil {
		return 0, err
	}
	var available uint64
	err = windows.GetDiskFreeSpaceEx(path, &available, nil, nil)
	return available, err
}
