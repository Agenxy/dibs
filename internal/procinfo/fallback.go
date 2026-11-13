// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package procinfo

import (
	"log/slog"
	"runtime"
	"strings"
	"sync/atomic"
)

// FallbackOperation fixes the diagnostic's cardinality independently of PIDs.
// No process evidence or participant data is retained by these counters.
type FallbackOperation uint8

// Native observation kinds with independently counted command fallbacks.
const (
	FallbackPath FallbackOperation = iota
	FallbackOpenFiles
	FallbackDiscovery
	FallbackCommand
	FallbackCPU
	FallbackPeerStart
	fallbackOperationCount
)

var fallbackNames = [fallbackOperationCount]string{
	"process executable path", "process open files", "process discovery",
	"process command line", "process CPU", "peer process start",
}

var fallbackCounts [fallbackOperationCount]atomic.Uint64

// WarnFallback records one fallback, warning once per operation for this process.
// pid is zero for a whole-table observation. Comm is a best-effort kernel name
// at warning time, never argv, a path, or identity evidence. An unreadable name
// stays unknown; diagnostics never try a command to obtain it.
//
// Debug counts are cumulative, emitted at powers of two (2, 4, 8, ...), so a
// recurring fallback is visible without a line per sweep, a timer or a PID cache.
// The caller still runs exactly its existing fallback for this pass.
func WarnFallback(operation FallbackOperation, pid int, err error) {
	if operation >= fallbackOperationCount {
		return
	}
	count := nextFallbackCount(&fallbackCounts[operation])
	if count == 1 {
		comm := "unknown"
		if pid > 0 {
			if name, nameErr := processComm(pid); nameErr == nil && name != "" {
				comm = name
			}
		}
		slog.Warn("native process observation unavailable; using command fallback",
			"operation", fallbackNames[operation], "pid", pid, "comm", comm,
			"callers", fallbackCallers(), "fallback_count", count, "err", err)
	} else if count != 0 && count&(count-1) == 0 {
		slog.Debug("native process command fallback count",
			"operation", fallbackNames[operation], "fallback_count", count)
	}
}

// Saturation preserves the once-only warning even if a counter exhausts uint64.
func nextFallbackCount(counter *atomic.Uint64) uint64 {
	for {
		previous := counter.Load()
		if previous == ^uint64(0) {
			return previous
		}
		if counter.CompareAndSwap(previous, previous+1) {
			return previous + 1
		}
	}
}

func fallbackCallers() []string {
	var pcs [8]uintptr
	// Skip runtime.Callers, this helper and WarnFallback; keep its real caller.
	n := runtime.Callers(3, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	callers := make([]string, 0, len(pcs))
	for len(callers) < len(pcs) {
		frame, more := frames.Next()
		name := strings.TrimPrefix(frame.Function, "github.com/agenxy/dibs/")
		if len(name) > 192 {
			name = name[:192]
		}
		callers = append(callers, name)
		if !more {
			break
		}
	}
	return callers
}
