//go:build windows

package paths

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// LockExclusive is LockFileEx over the first byte of f: an exclusive lock,
// mandatory rather than advisory on this platform, which is fine for a file
// nothing reads except to take the lock.
func LockExclusive(f *os.File, wait bool) error {
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK)
	if !wait {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	var ol windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, &ol)
}

// Unlock releases the byte LockExclusive locked.
func Unlock(f *os.File) {
	var ol windows.Overlapped
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &ol)
}

// LockHeldElsewhere is ERROR_LOCK_VIOLATION, which is what a
// LOCKFILE_FAIL_IMMEDIATELY request gets when somebody else holds the lock.
func LockHeldElsewhere(err error) bool { return errors.Is(err, windows.ERROR_LOCK_VIOLATION) }
