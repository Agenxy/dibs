//go:build linux

package main

import (
	"context"
	"os"
	"syscall"
)

// watchParent returns when ppid exits.
//
// PR_SET_PDEATHSIG first, because it is the only mechanism here that survives
// this process being wedged: the kernel delivers SIGTERM whether or not any
// goroutine is running. The poll below is what turns that into a channel the
// shutdown path can select on, and what covers the case where prctl is refused.
func watchParent(ctx context.Context, ppid int) {
	const prSetPdeathsig = 1
	_, _, _ = syscall.RawSyscall(syscall.SYS_PRCTL, prSetPdeathsig, uintptr(syscall.SIGTERM), 0)
	// The signal is armed against whoever the parent is NOW. If it changed in
	// the window above, this process is already an orphan.
	if os.Getppid() != ppid {
		return
	}
	pollParent(ctx, ppid)
}
