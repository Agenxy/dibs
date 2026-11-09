// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// Package libproc binds the documented Darwin process APIs through libSystem,
// without cgo. The trampoline/linkname pattern follows the pinned x/sys/unix
// Darwin backend; no kernel syscall numbers or optional runtime library apply.
package libproc

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"syscall"
	"unsafe"
)

// ErrUnavailable means libproc supplied no readable bytes. It does not prove
// absence: a zero return can also mean an empty descriptor inventory.
var ErrUnavailable = errors.New("libproc returned no readable process information")

// syscall.syscall6 is push-exposed by the standard library for the libSystem
// ABI used by x/sys. The default linkname checker remains enabled in builds.
//
//go:linkname systemCall6 syscall.syscall6
func systemCall6(fn, a1, a2, a3, a4, a5, a6 uintptr) (uintptr, uintptr, syscall.Errno)

var (
	pidInfoAddr   uintptr
	pidFDInfoAddr uintptr
	pidPathAddr   uintptr
)

//go:cgo_import_dynamic dibs_proc_pidinfo proc_pidinfo "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic dibs_proc_pidfdinfo proc_pidfdinfo "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic dibs_proc_pidpath proc_pidpath "/usr/lib/libSystem.B.dylib"

// PIDInfo reads a bounded process-info structure or descriptor inventory.
func PIDInfo(pid, flavor int, arg uint64, buf []byte) (int, error) {
	return withBuffer(pid, buf, func(ptr uintptr) uintptr {
		n, _, _ := systemCall6(pidInfoAddr, uintptr(pid), uintptr(flavor), uintptr(arg), ptr, uintptr(len(buf)), 0)
		return n
	})
}

// PIDFDInfo reads a bounded descriptor-info structure.
func PIDFDInfo(pid, fd, flavor int, buf []byte) (int, error) {
	if fd < 0 || fd > math.MaxInt32 {
		return 0, syscall.EINVAL
	}
	return withBuffer(pid, buf, func(ptr uintptr) uintptr {
		n, _, _ := systemCall6(pidFDInfoAddr, uintptr(pid), uintptr(fd), uintptr(flavor), ptr, uintptr(len(buf)), 0)
		return n
	})
}

// PIDPath reads the executable vnode path, returning its byte length.
func PIDPath(pid int, buf []byte) (int, error) {
	return withBuffer(pid, buf, func(ptr uintptr) uintptr {
		n, _, _ := systemCall6(pidPathAddr, uintptr(pid), ptr, uintptr(len(buf)), 0, 0, 0)
		return n
	})
}

func withBuffer(pid int, buf []byte, invoke func(uintptr) uintptr) (int, error) {
	if pid <= 0 || pid > math.MaxInt32 || len(buf) == 0 || len(buf) > math.MaxInt32 {
		return 0, syscall.EINVAL
	}
	var pin runtime.Pinner
	pin.Pin(&buf[0])
	defer pin.Unpin()
	// Zero can mean either refusal or an empty FD inventory. Both remain
	// unknown here: the caller's independent incarnation observation decides
	// presence, and its bounded per-PID command fallback supplies missing data.
	// #nosec G103 -- the live byte buffer is pinned for the entire synchronous C call.
	n := invoke(uintptr(unsafe.Pointer(&buf[0])))
	runtime.KeepAlive(buf)
	if n == 0 {
		return 0, ErrUnavailable
	}
	if n > uintptr(len(buf)) {
		return 0, fmt.Errorf("libproc returned %d bytes for a %d-byte buffer", n, len(buf))
	}
	return int(n), nil
}
