// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build unix

package liveness

import "syscall"

// ObservePID distinguishes an absent process from an observation failure.
// Only ESRCH proves absence; permission denial proves the process exists.
func (p *Poller) ObservePID(pid int) (live, known bool) {
	if pid <= 0 {
		return false, true
	}
	err := syscall.Kill(pid, 0)
	switch err {
	case nil, syscall.EPERM:
		return true, true
	case syscall.ESRCH:
		return false, true
	default:
		return false, false
	}
}
