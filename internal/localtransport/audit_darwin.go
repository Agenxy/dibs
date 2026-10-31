// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package localtransport

import (
	"net"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

func peerAuditToken(conn net.Conn) []byte {
	c, ok := conn.(*net.UnixConn)
	if !ok {
		return nil
	}
	raw, err := c.SyscallConn()
	if err != nil {
		return nil
	}
	var token [32]byte
	length := uint32(len(token))
	var errno syscall.Errno
	err = raw.Control(func(fd uintptr) {
		// LOCAL_PEERTOKEN returns the connect-time audit token, including
		// pidversion. Never reconstruct this authority from a supplied PID.
		// x/sys exposes no binary-buffer wrapper; its string wrapper truncates
		// at NUL. This fixed-size kernel result is checked before use.
		// #nosec G103 -- bounded 32-byte audit token and socklen, on a held Unix fd.
		_, _, errno = syscall.Syscall6(syscall.SYS_GETSOCKOPT, fd, unix.SOL_LOCAL, unix.LOCAL_PEERTOKEN,
			uintptr(unsafe.Pointer(&token[0])), uintptr(unsafe.Pointer(&length)), 0)
	})
	if err != nil || errno != 0 || length != uint32(len(token)) {
		return nil
	}
	return token[:]
}
