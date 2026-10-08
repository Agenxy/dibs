// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build linux

package paths

import "syscall"

func ctimeOf(st *syscall.Stat_t) int64 {
	// Widened first: the fields are int32 on some architectures.
	return int64(st.Ctim.Sec)*1e9 + int64(st.Ctim.Nsec)
}
