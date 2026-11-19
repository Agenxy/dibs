// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package testexec

import "golang.org/x/sys/unix"

func kernelZombie(pid int) (bool, error) {
	row, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	// sys/proc.h: SZOMB == 5, awaiting collection by parent.
	return err == nil && int(row.Proc.P_pid) == pid && row.Proc.P_stat == 5, err
}
