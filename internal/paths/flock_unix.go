// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build unix

package paths

import (
	"errors"
	"os"
	"syscall"
)

// LockExclusive takes an exclusive advisory lock on f, waiting when wait is
// set and failing at once otherwise. flock on unix; see flock_windows.go for
// the other spelling of the same three calls.
func LockExclusive(f *os.File, wait bool) error {
	how := syscall.LOCK_EX
	if !wait {
		how |= syscall.LOCK_NB
	}
	return syscall.Flock(int(f.Fd()), how)
}

// LockShared allows concurrent readers while excluding a configuration writer.
func LockShared(f *os.File, wait bool) error {
	flags := syscall.LOCK_SH
	if !wait {
		flags |= syscall.LOCK_NB
	}
	return syscall.Flock(int(f.Fd()), flags)
}

// Unlock releases a lock LockExclusive took. Errors are not reported: the
// file is closed right after, which releases it anyway.
func Unlock(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }

// LockHeldElsewhere reports whether a non-waiting LockExclusive failed because
// another process holds the lock, as opposed to failing at all.
func LockHeldElsewhere(err error) bool { return errors.Is(err, syscall.EWOULDBLOCK) }
