// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build !darwin && !linux

package harnessenv

import "time"

// Preserve the existing platform path outside the Unix release targets.
func nativeSelfStart() (time.Time, bool) { return time.Time{}, false }
