//go:build darwin

package paths

import (
	"bytes"
	"os"
	"syscall"
	"unsafe"
)

// EvalSymlinks does not correct case on an insensitive filesystem. Ask the
// opened object for its native path, then check it still names that object.
// Failure keeps today's spelling; this is filesystem evidence, not authority.
func nativeSpelling(p string) string {
	f, err := os.OpenFile(p, syscall.O_EVTONLY, 0) // #nosec G304 -- local canonicalization; event-only cannot block on a FIFO
	if err != nil {
		return p
	}
	defer func() { _ = f.Close() }()
	var buf [1024]byte // Darwin MAXPATHLEN, required by F_GETPATH
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_GETPATH, uintptr(unsafe.Pointer(&buf[0])))
	if errno != 0 {
		return p
	}
	end := bytes.IndexByte(buf[:], 0)
	if end <= 0 {
		return p
	}
	native := string(buf[:end])
	opened, err := f.Stat()
	if err != nil {
		return p
	}
	current, err := os.Stat(native)
	if err != nil || !os.SameFile(opened, current) {
		return p
	}
	return native
}
