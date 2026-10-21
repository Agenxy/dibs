// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"errors"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func nativeSelfStart() (time.Time, bool) {
	auxv, err := unix.Auxv()
	if err != nil {
		return time.Time{}, true
	}
	var ticksPerSecond uint64
	for _, entry := range auxv {
		if entry[0] == 17 { // Linux ELF AT_CLKTCK, not a guessed USER_HZ
			ticksPerSecond = uint64(entry[1])
			break
		}
	}
	stat, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return time.Time{}, true
	}
	system, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, true
	}
	started, err := linuxProcessStart(stat, system, ticksPerSecond)
	if err != nil {
		return time.Time{}, true
	}
	return started, true
}

func linuxProcessStart(stat, system []byte, ticksPerSecond uint64) (time.Time, error) {
	// comm can contain spaces and parentheses; the remaining fields cannot.
	end := strings.LastIndexByte(string(stat), ')')
	if end < 0 || ticksPerSecond == 0 {
		return time.Time{}, errors.New("process stat or clock tick rate unavailable")
	}
	fields := strings.Fields(string(stat[end+1:]))
	if len(fields) < 20 { // state is field 3; starttime is field 22
		return time.Time{}, errors.New("process stat has no start time")
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	var boot int64
	for _, line := range strings.Split(string(system), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 && parts[0] == "btime" {
			boot, err = strconv.ParseInt(parts[1], 10, 64)
			break
		}
	}
	seconds := ticks / ticksPerSecond
	if err != nil || boot <= 0 || seconds > uint64(math.MaxInt64-boot) {
		return time.Time{}, errors.New("boot time or process start time invalid")
	}
	return time.Unix(boot+int64(seconds), 0), nil
}
