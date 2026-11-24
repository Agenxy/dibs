// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build !darwin

package harnessenv

import "context"

// No command recovers an executable path that /proc refused. Running ps for
// each unreadable ancestor (a root sshd above a dibd started over ssh) spawned
// six children per idle sweep and learned only a task name. Found on the first
// native riscv64 run.
var platformPathFallback func(context.Context, int) (string, error)

var _ = psPathFallback // shared with darwin; referenced so the linter sees it used
