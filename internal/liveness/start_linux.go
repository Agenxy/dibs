// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build linux

package liveness

import (
	"os"
	"strconv"
)

// StartMillis is when pid started, in unix milliseconds: boot time from
// /proc/stat plus the process's starttime ticks. Both are fixed for the life
// of the process, so two readings agree exactly, which is what lets the sweep
// compare them for equality.
func (p *Poller) StartMillis(pid int) (int64, bool) {
	if pid <= 0 {
		return 0, false
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	ticks, ok := parseStartTicks(string(stat))
	if !ok {
		return 0, false
	}
	all, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, false
	}
	boot, ok := parseBootTime(string(all))
	if !ok {
		return 0, false
	}
	return boot*1000 + ticks*1000/userHZ, true
}
