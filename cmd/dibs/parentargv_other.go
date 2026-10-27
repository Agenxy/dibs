// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build !darwin && !linux

package main

import "errors"

// parentArgv is unknown here, so channel mode stays off and the existing
// delivery routes apply.
func parentArgv() []string { return nil }

// processExecutable is only needed for the macOS app service.
func processExecutable(int) (string, error) {
	return "", errors.New("process executable is not read on this platform")
}
