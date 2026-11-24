// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
)

// On Linux `ps -o comm=` prints /proc/<pid>/comm, which any user may read,
// so the fallback reads it directly. Running ps instead started a child for
// every root-owned ancestor whose exe link /proc refused (sshd above a dibd
// started over ssh): six per idle sweep. Found on the first native riscv64
// run, by TestIdleProcessObservationStartsNoChildren.
func platformPathFallback(_ context.Context, pid int) (string, error) {
	out, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
	if err != nil {
		return "", err
	}
	if len(out) > 4096 {
		return "", errors.New("process name exceeds bound")
	}
	return strings.TrimSpace(string(out)), nil
}

var _ = psPathFallback // the non-Linux fallback; kept in the shared file
