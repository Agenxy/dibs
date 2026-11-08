// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package liveness

import (
	"testing"
	"time"
)

// On Linux the processor time comes from /proc in clock ticks, not from a ps
// column that rounds to whole seconds. Fifty milliseconds is five ticks and
// must read as fifty milliseconds: at `--min-duty 0.05` and a one-second age
// that is the difference between "thinking" and a false "stuck" (issue #4).
func TestProcStatKeepsSubSecondProcessorTime(t *testing.T) {
	// A real line shape, with a comm that contains a space AND parentheses,
	// which is why the split is from the last ')' and not the first space.
	line := "4242 (dibs (worker) x) R 1 4242 4242 0 -1 4194304 120 0 0 0 " +
		"3 2 0 0 20 0 1 0 123456 10485760 300 18446744073709551615 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0"
	cpu, elapsed, ok := parseProcStat(line, 1300.5)
	if !ok {
		t.Fatal("a well-formed stat line did not parse")
	}
	if cpu != 50*time.Millisecond {
		t.Errorf("cpu = %v, want 50ms: utime 3 + stime 2 ticks at USER_HZ 100; ps would "+
			"have said 0", cpu)
	}
	// starttime 123456 ticks = 1234.56 s after boot; uptime 1300.5 s.
	if want := time.Duration((1300.5 - 1234.56) * float64(time.Second)); elapsed < want-time.Millisecond || elapsed > want+time.Millisecond {
		t.Errorf("elapsed = %v, want %v", elapsed, want)
	}
	if _, _, ok := parseProcStat("garbage", 1); ok {
		t.Error("garbage parsed")
	}
	if up, ok := parseProcUptime("1300.50 5100.20\n"); !ok || up != 1300.5 {
		t.Errorf("uptime = %v %v", up, ok)
	}
}

func TestStartTicksAndBootTimeParse(t *testing.T) {
	line := "4242 (a) b) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 987654 20 21"
	if got, ok := parseStartTicks(line); !ok || got != 987654 {
		t.Errorf("parseStartTicks = %d, %v; want field 22 past the last ')'", got, ok)
	}
	if _, ok := parseStartTicks("4242 no paren"); ok {
		t.Error("a line with no comm field parsed")
	}
	if got, ok := parseBootTime("cpu 1 2\nbtime 1700000000\nprocesses 9\n"); !ok || got != 1700000000 {
		t.Errorf("parseBootTime = %d, %v", got, ok)
	}
	if _, ok := parseBootTime("cpu 1 2\n"); ok {
		t.Error("a /proc/stat with no btime parsed")
	}
}
