// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build !darwin && !linux

package testport

func outageEphemeralRange() (int, int, bool, error) { return 0, 0, false, nil }
