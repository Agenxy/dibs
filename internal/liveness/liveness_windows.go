//go:build windows

package liveness

import (
	"math"

	"golang.org/x/sys/windows"
)

type Poller struct{}

func New() *Poller { return &Poller{} }

// Alive asks the kernel whether the process still runs: open it with the
// least rights that answer, and wait on it for zero milliseconds. A running
// process is not signalled and the wait times out; an exited one is
// signalled at once. That is the only conclusive test: the exit code is not,
// because 259 (STILL_ACTIVE) is also a code a process can exit with.
//
// A process this user may not open is treated as alive, as EPERM is on
// unix: it exists, and that is the question. A pid past what the kernel can
// name is nobody's process, not a truncated one.
func (p *Poller) Alive(pid int) bool {
	if pid <= 0 || pid > math.MaxUint32 {
		return false
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid)) // #nosec G115 -- bounded above
	if err != nil {
		return err == windows.ERROR_ACCESS_DENIED
	}
	defer func() { _ = windows.CloseHandle(h) }()
	ev, err := windows.WaitForSingleObject(h, 0)
	if err != nil {
		return false
	}
	return ev == uint32(windows.WAIT_TIMEOUT) //nolint:unconvert // WAIT_TIMEOUT is an Errno constant; the event is a uint32
}
