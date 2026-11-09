// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build !darwin && !linux

package procinfo

import (
	"context"
	"time"
)

// Native reports whether this OS has an in-process identity/inventory backend.
const Native = false

// SameProcess is unavailable on this platform.
func SameProcess(Process) (bool, error) { return false, ErrUnsupported }

// Command is unavailable on this platform.
func Command(int) ([]string, error) { return nil, ErrUnsupported }

// Environ is unavailable on this platform.
func Environ(int) ([]string, error) { return nil, ErrUnsupported }

// Snapshot is unavailable on this platform.
func Snapshot(context.Context) ([]Process, error) { return nil, ErrUnsupported }

// Start is unavailable on this platform.
func Start(int) (time.Time, error) { return time.Time{}, ErrUnsupported }

// Path is unavailable on this platform.
func Path(int) (string, error) { return "", ErrUnsupported }

// Arguments is unavailable on this platform.
func Arguments(int) ([]string, []string, error) { return nil, nil, ErrUnsupported }

// CPU is unavailable on this platform.
func CPU(int) (time.Duration, error) { return 0, ErrUnsupported }

// OpenFiles is unavailable on this platform.
func OpenFiles(int, int) ([]string, error) { return nil, ErrUnsupported }
