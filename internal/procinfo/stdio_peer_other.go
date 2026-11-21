// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build !darwin

package procinfo

// OwnsStdioPipes is unavailable outside Darwin.
func OwnsStdioPipes(int, int) bool { return false }
