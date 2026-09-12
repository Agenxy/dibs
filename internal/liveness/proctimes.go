package liveness

import (
	"strconv"
	"strings"
	"time"
)

// userHZ is the unit /proc/<pid>/stat reports times in. It is a userspace
// ABI constant, 100 on every Linux, independent of the kernel's own tick
// rate: the kernel scales to it precisely so readers need not ask.
const userHZ = 100

// parseProcStat reads processor time and age out of one /proc/<pid>/stat
// line, given the machine's uptime in seconds.
//
// The comm field (2) is in parentheses and may itself contain spaces and
// parentheses, so the line is split after the LAST ')' rather than on
// whitespace from the start. After it, field 3 is the state; utime and
// stime are fields 14 and 15, starttime field 22, all in USER_HZ ticks.
// Pure, so it can be tested on the machine this was written on, which is
// not Linux.
func parseProcStat(line string, uptime float64) (cpu, elapsed time.Duration, ok bool) {
	i := strings.LastIndex(line, ")")
	if i < 0 {
		return 0, 0, false
	}
	f := strings.Fields(line[i+1:])
	// f[0] is field 3 (state), so field N is f[N-3].
	if len(f) < 20 {
		return 0, 0, false
	}
	// Signed, because time.Duration is: a tick count past int64 is not a
	// process, it is a corrupt line, and ParseInt refuses it for us.
	utime, err1 := strconv.ParseInt(f[14-3], 10, 64)
	stime, err2 := strconv.ParseInt(f[15-3], 10, 64)
	start, err3 := strconv.ParseInt(f[22-3], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || utime < 0 || stime < 0 || start < 0 {
		return 0, 0, false
	}
	cpu = time.Duration(utime+stime) * time.Second / userHZ
	age := uptime - float64(start)/userHZ
	if age < 0 {
		age = 0
	}
	return cpu, time.Duration(age * float64(time.Second)), true
}

// parseProcUptime reads the first number of /proc/uptime: seconds since boot.
func parseProcUptime(s string) (float64, bool) {
	f := strings.Fields(s)
	if len(f) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(f[0], 64)
	return v, err == nil
}
