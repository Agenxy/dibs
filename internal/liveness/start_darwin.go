// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin

package liveness

import "golang.org/x/sys/unix"

// StartMillis is when pid started, in unix milliseconds, from the kernel's
// own process table: no fork, so the sweep can ask it of every agent.
func (p *Poller) StartMillis(pid int) (int64, bool) {
	if pid <= 0 {
		return 0, false
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(info.Proc.P_pid) != pid || info.Proc.P_starttime.Sec <= 0 {
		return 0, false
	}
	return info.Proc.P_starttime.Sec*1000 + int64(info.Proc.P_starttime.Usec)/1000, true
}
