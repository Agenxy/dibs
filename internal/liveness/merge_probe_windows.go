// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build windows

package liveness

import (
	"math"

	"golang.org/x/sys/windows"
)

// ObservePID preserves unknown errors rather than treating a failed query as
// permission to retire a participant. A signalled process handle has exited.
func (p *Poller) ObservePID(pid int) (live, known bool) {
	if pid <= 0 || pid > math.MaxUint32 {
		return false, true
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid)) // #nosec G115 -- bounded above
	if err != nil {
		switch err {
		case windows.ERROR_ACCESS_DENIED:
			return true, true
		case windows.ERROR_INVALID_PARAMETER:
			return false, true
		default:
			return false, false
		}
	}
	defer func() { _ = windows.CloseHandle(h) }()
	state, err := windows.WaitForSingleObject(h, 0)
	if err != nil {
		return false, false
	}
	if state == uint32(windows.WAIT_TIMEOUT) {
		return true, true
	} //nolint:unconvert // WAIT_TIMEOUT is Errno, state is uint32
	return false, state == windows.WAIT_OBJECT_0
}
