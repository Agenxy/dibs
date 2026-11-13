// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// Package procinfo reads this kernel's process evidence without starting a
// child. A Snapshot belongs to one observation pass, never a cross-tick cache.
// Callers keep host identity beside PIDs; nothing here observes another host.
package procinfo

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// Process is one row in a kernel inventory. Comm is a short display name,
// never executable-path or ancestry evidence.
type Process struct {
	PID, PPID   int
	Start       time.Time
	Comm        string
	startTicks  int64 // Linux's boot-relative incarnation, scoped to the snapshot
	bootSeconds int64 // the same pass's /proc/stat reading
}

// Stamp matches the bridge's normalized UTC, whole-second incarnation claim.
// Millisecond or full-resolution comparisons must use Start itself instead.
func Stamp(start time.Time) string {
	if start.IsZero() {
		return ""
	}
	return start.UTC().Format("Mon Jan 2 15:04:05 2006")
}

// ErrUnsupported asks the caller to retain its existing platform fallback.
var ErrUnsupported = errors.New("native process observation is unsupported")

// ErrUnavailable marks evidence the kernel did not make readable this pass.
var ErrUnavailable = errors.New("native process evidence is unavailable")

// Gone distinguishes a kernel observation of absence from an unavailable ABI.
func Gone(err error) bool { return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) }
