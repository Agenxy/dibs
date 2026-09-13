//go:build linux

package liveness

import (
	"os"
	"strconv"
	"time"
)

// processTimes on Linux reads /proc, where processor time is in clock ticks
// (USER_HZ, 100 a second) rather than the whole seconds procps prints. A
// process that has burned 50 ms is five ticks here and `00:00:00` there, and
// the difference decided a wrong verdict at short settings (issue #4). ps
// stays as the fallback for a /proc that cannot be read.
func processTimes(pid int) (cpu, elapsed time.Duration) {
	if pid <= 0 {
		return 0, 0
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return psTimes(pid)
	}
	up, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return psTimes(pid)
	}
	uptime, ok := parseProcUptime(string(up))
	if !ok {
		return psTimes(pid)
	}
	cpu, elapsed, ok = parseProcStat(string(stat), uptime)
	if !ok {
		return psTimes(pid)
	}
	return cpu, elapsed
}
