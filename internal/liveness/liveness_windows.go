//go:build windows

package liveness

import "golang.org/x/sys/windows"

type Poller struct{}

func New() *Poller { return &Poller{} }

// Alive asks the kernel whether the process still runs: open it with the
// least right that answers, read its exit code, and STILL_ACTIVE means alive.
// A process this user may not open is treated as alive, as EPERM is on unix:
// it exists, and that is the question.
func (p *Poller) Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid)) // #nosec G115 -- a pid
	if err != nil {
		return err == windows.ERROR_ACCESS_DENIED
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == 259 // STILL_ACTIVE
}
