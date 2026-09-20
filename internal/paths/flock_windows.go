//go:build windows

package paths

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockOffset is the byte LockExclusive locks: far past any body one of these
// files holds, and a range may be locked beyond EOF.
//
// LockFileEx is MANDATORY on this platform, not advisory: a byte range one
// handle holds exclusively cannot be read through another. The lock used to
// cover byte 0 of the registration JSON, so `describe` (os.ReadFile through a
// fresh handle) was refused for every LIVE daemon and reported each as
// Unknown, and stop and upgrade, which identify a daemon by the directory in
// that JSON, could not find the one that was running. The lock is the
// liveness signal; the body has to stay readable beside it. Found by the
// pre-release review, round four.
const lockOffset = 1 << 40

// LockExclusive is LockFileEx over one byte of f at lockOffset: an exclusive
// lock that says "this process is alive" without denying anybody the file's
// contents.
func LockExclusive(f *os.File, wait bool) error {
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK)
	if !wait {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	ol := lockRange()
	return windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, &ol)
}

// Unlock releases the byte LockExclusive locked.
func Unlock(f *os.File) {
	ol := lockRange()
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &ol)
}

func lockRange() windows.Overlapped {
	return windows.Overlapped{
		Offset:     uint32(lockOffset & 0xFFFFFFFF),
		OffsetHigh: uint32(lockOffset >> 32),
	}
}

// LockHeldElsewhere is ERROR_LOCK_VIOLATION, which is what a
// LOCKFILE_FAIL_IMMEDIATELY request gets when somebody else holds the lock.
func LockHeldElsewhere(err error) bool { return errors.Is(err, windows.ERROR_LOCK_VIOLATION) }
