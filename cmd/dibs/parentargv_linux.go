// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build linux

package main

import (
	"bytes"
	"os"
	"strconv"
)

// parentArgv reads the parent's arguments from /proc.
func parentArgv() []string {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(os.Getppid()) + "/cmdline") //nolint:gosec // numeric pid
	if err != nil || len(raw) == 0 {
		return nil
	}
	parts := bytes.Split(bytes.TrimRight(raw, "\x00"), []byte{0})
	argv := make([]string, len(parts))
	for i, p := range parts {
		argv[i] = string(p)
	}
	return argv
}
