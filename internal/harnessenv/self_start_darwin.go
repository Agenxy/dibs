// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func nativeSelfStart() (time.Time, bool) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", os.Getpid())
	if err != nil || int(info.Proc.P_pid) != os.Getpid() || info.Proc.P_starttime.Sec <= 0 {
		return time.Time{}, true // unavailable evidence never falls back to exec
	}
	return time.Unix(info.Proc.P_starttime.Sec, int64(info.Proc.P_starttime.Usec)*1000), true
}
