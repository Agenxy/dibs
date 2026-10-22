// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package testport

import "golang.org/x/sys/unix"

func outageEphemeralRange() (int, int, bool, error) {
	first, err := unix.SysctlUint32("net.inet.ip.portrange.first")
	if err != nil {
		return 0, 0, true, err
	}
	last, err := unix.SysctlUint32("net.inet.ip.portrange.last")
	return int(first), int(last), true, err
}
