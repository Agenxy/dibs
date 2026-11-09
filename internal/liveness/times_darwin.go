// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package liveness

import (
	"errors"
	"fmt"
	"time"

	"github.com/agenxy/dibs/internal/procinfo"
)

var nativeCPU = procinfo.CPU

func processTimes(pid int) (cpu, elapsed time.Duration) {
	if pid <= 0 {
		return 0, 0
	}
	started, err := procinfo.Start(pid)
	if procinfo.Gone(err) {
		return 0, 0
	}
	if err == nil {
		cpu, err = nativeCPU(pid)
		if err == nil {
			current, checkErr := procinfo.Start(pid)
			if checkErr == nil && current.Equal(started) {
				elapsed = time.Since(started)
				if elapsed < 0 {
					elapsed = 0
				}
				return cpu, elapsed
			}
			err = errors.New("process incarnation changed while sampling")
			if checkErr != nil {
				err = fmt.Errorf("process incarnation unavailable while sampling: %w", checkErr)
			}
		}
	}
	procinfo.WarnFallback("process CPU", err)
	return psTimes(pid)
}
