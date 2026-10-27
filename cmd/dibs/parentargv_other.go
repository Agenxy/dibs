// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build !darwin && !linux

package main

// parentArgv is unknown here, so channel mode stays off and the existing
// delivery routes apply.
func parentArgv() []string { return nil }
