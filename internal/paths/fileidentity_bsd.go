// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build unix && !darwin && !linux

package paths

import "syscall"

// ctimeOf on the unix targets Dibs does not release for: no change time,
// so the identity is device, inode and mtime. Enough to compile there; a
// recreated checkout is caught by mtime alone, as on Windows before its
// file index was read.
func ctimeOf(st *syscall.Stat_t) int64 { _ = st; return 0 }
