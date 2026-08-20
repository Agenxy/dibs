//go:build darwin

package main

import (
	"context"
	"os"
	"syscall"
)

// watchParent returns when ppid exits, using kqueue's process filter.
//
// Event-driven rather than polled: the kernel knows the moment the parent goes,
// and a bridge that lingers is one more process holding a stream open.
func watchParent(ctx context.Context, ppid int) {
	kq, err := syscall.Kqueue()
	if err != nil {
		pollParent(ctx, ppid) // no kqueue: fall back rather than never notice
		return
	}
	defer func() { _ = syscall.Close(kq) }()

	ev := syscall.Kevent_t{
		Ident:  uint64(ppid), // #nosec G115 -- a pid from os.Getppid(), always positive
		Filter: syscall.EVFILT_PROC,
		Flags:  syscall.EV_ADD | syscall.EV_ONESHOT,
		Fflags: syscall.NOTE_EXIT,
	}
	if _, err := syscall.Kevent(kq, []syscall.Kevent_t{ev}, nil, nil); err != nil {
		// ESRCH means it is already gone, which is the answer, not a failure.
		return
	}
	// Registered, so re-check: the parent may have exited in the window above,
	// and a one-shot registration for a dead pid never fires.
	if os.Getppid() != ppid {
		return
	}
	// Cancellation is why this waits in bounded slices rather than forever: the
	// process is exiting anyway on the other paths, and a goroutine blocked in
	// a syscall cannot be told about it.
	out := make([]syscall.Kevent_t, 1)
	timeout := syscall.Timespec{Sec: 5}
	for {
		if ctx.Err() != nil {
			return
		}
		n, err := syscall.Kevent(kq, nil, out, &timeout)
		if err != nil && err != syscall.EINTR {
			pollParent(ctx, ppid)
			return
		}
		if n > 0 || os.Getppid() != ppid {
			return
		}
	}
}
