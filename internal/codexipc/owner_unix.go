//go:build unix

// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package codexipc

import (
	"os"
	"syscall"
)

func ownedByUser(info os.FileInfo) bool {
	s, ok := info.Sys().(*syscall.Stat_t)
	return ok && uint64(s.Uid) == uint64(os.Getuid()) // #nosec G115 -- Unix uid is nonnegative.
}
