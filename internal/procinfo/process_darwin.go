// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package procinfo

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/agenxy/dibs/internal/procinfo/libproc"

	"golang.org/x/sys/unix"
)

// Darwin's documented libproc APIs are bound through libSystem without cgo.
// Every error/short structure returns to the caller for its warned command
// fallback on that pass. The per-arch bindings are checked on both Mac arches.
const (
	Native          = true
	pidInfoCall     = 2
	pidFDCall       = 3
	listFDs         = 1
	taskInfo        = 4
	pathInfo        = 11
	vnodePathInfo   = 2
	taskInfoSize    = 96
	vnodePathSize   = 1200
	vnodePathOffset = 176
	maxProcessFiles = 65536
	zombieState     = 5 // sys/proc.h SZOMB: exited, awaiting collection by parent
)

// The seam replaces only the kernel boundary, including short successful
// replies. It does not bypass the production length or decoding checks.
var readInfo = func(call, pid, flavor, arg int, buf []byte) (int, error) {
	if pid <= 0 || len(buf) == 0 {
		return 0, unix.EINVAL
	}
	switch call {
	case pidInfoCall:
		if flavor == pathInfo {
			n, err := libproc.PIDPath(pid, buf)
			if err != nil {
				return 0, err
			}
			if n == 0 {
				return 0, fmt.Errorf("libproc executable path is unavailable")
			}
			return 0, nil // path decoding uses its checked NUL terminator, not a struct size
		}
		if arg < 0 {
			return 0, unix.EINVAL
		}
		return libproc.PIDInfo(pid, flavor, uint64(arg), buf)
	case pidFDCall:
		return libproc.PIDFDInfo(pid, arg, flavor, buf)
	default:
		return 0, ErrUnsupported
	}
}

// Snapshot takes a fresh kernel table once. Consumers share these rows within
// their pass, and discard them afterwards. No process evidence is cached here.
func Snapshot(ctx context.Context) ([]Process, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	out := make([]Process, 0, len(rows))
	for _, row := range rows {
		if row.Proc.P_pid <= 0 || row.Proc.P_starttime.Sec <= 0 || row.Proc.P_stat == zombieState {
			continue
		}
		out = append(out, Process{
			PID: int(row.Proc.P_pid), PPID: int(row.Eproc.Ppid),
			Start: time.Unix(row.Proc.P_starttime.Sec, int64(row.Proc.P_starttime.Usec)*1000),
			Comm:  unix.ByteSliceToString(row.Proc.P_comm[:]),
		})
	}
	return out, ctx.Err()
}

// Start reads a live kernel incarnation, retaining microsecond precision.
// An unreaped zombie retains a PID/start record, but no executable process.
func Start(pid int) (time.Time, error) {
	if pid <= 0 {
		return time.Time{}, unix.ESRCH
	}
	row, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if errors.Is(err, unix.EIO) && errors.Is(unix.Kill(pid, 0), unix.ESRCH) {
			return time.Time{}, unix.ESRCH
		}
		return time.Time{}, err
	}
	if int(row.Proc.P_pid) != pid || row.Proc.P_starttime.Sec <= 0 || row.Proc.P_stat == zombieState {
		return time.Time{}, unix.ESRCH
	}
	return time.Unix(row.Proc.P_starttime.Sec, int64(row.Proc.P_starttime.Usec)*1000), nil
}

// SameProcess rechecks a row's incarnation after its other observations.
func SameProcess(row Process) (bool, error) {
	started, err := Start(row.PID)
	if Gone(err) {
		return false, nil
	}
	return started.Equal(row.Start), err
}

// Path reads the kernel's executable vnode, not argv[0], which a process can
// choose freely. The libproc adapter normalizes a successful path result to zero.
func Path(pid int) (string, error) {
	var buf [4096]byte
	n, err := readInfo(pidInfoCall, pid, pathInfo, 0, buf[:])
	if err != nil {
		return "", err
	}
	if n != 0 {
		return "", fmt.Errorf("executable path returned unexpected length %d", n)
	}
	end := bytes.IndexByte(buf[:], 0)
	if end <= 0 {
		return "", fmt.Errorf("executable path is empty or unterminated")
	}
	return string(buf[:end]), nil
}

// Arguments returns separated argv/environment, never a loggable raw buffer.
func Arguments(pid int) ([]string, []string, error) {
	if pid <= 0 {
		return nil, nil, unix.ESRCH
	}
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, nil, err
	}
	return parseArguments(raw)
}

// Command reads argv, keeping argument boundaries until the consumer formats it.
func Command(pid int) ([]string, error) { args, _, err := Arguments(pid); return args, err }

// Environ reads inherited environment entries without exposing them to logs.
func Environ(pid int) ([]string, error) {
	_, env, err := Arguments(pid)
	if err == nil && len(env) == 0 {
		return nil, ErrUnavailable
	}
	return env, err
}

func parseArguments(raw []byte) ([]string, []string, error) {
	if len(raw) < 5 {
		return nil, nil, fmt.Errorf("short KERN_PROCARGS2 result")
	}
	argc := int(binary.LittleEndian.Uint32(raw[:4]))
	if argc <= 0 || argc > len(raw)-4 {
		return nil, nil, fmt.Errorf("invalid KERN_PROCARGS2 argc")
	}
	raw = raw[4:]
	end := bytes.IndexByte(raw, 0) // executable path precedes argv padding
	if end < 0 {
		return nil, nil, fmt.Errorf("unterminated KERN_PROCARGS2 executable")
	}
	raw = bytes.TrimLeft(raw[end+1:], "\x00")
	args := make([]string, 0, argc)
	for range argc {
		end = bytes.IndexByte(raw, 0)
		if end < 0 {
			return nil, nil, fmt.Errorf("truncated KERN_PROCARGS2 argv")
		}
		args = append(args, string(raw[:end]))
		raw = raw[end+1:]
	}
	var env []string
	for len(raw) > 0 && raw[0] != 0 {
		end = bytes.IndexByte(raw, 0)
		if end < 0 {
			return nil, nil, fmt.Errorf("truncated KERN_PROCARGS2 environment")
		}
		env = append(env, string(raw[:end]))
		raw = raw[end+1:]
	}
	return args, env, nil
}

// CPU preserves subsecond CPU duty. PIDTASKINFO totals are absolute ticks,
// NOT nanoseconds: on measured Apple Silicon and Rosetta hw.tbfrequency is
// 24 MHz. sys/sysctl.h defines that frequency as the OS timing-services basis.
// Linux USER_HZ=100 and ps's platform-dependent TIME columns are different units.
func CPU(pid int) (time.Duration, error) {
	var buf [taskInfoSize]byte
	n, err := readInfo(pidInfoCall, pid, taskInfo, 0, buf[:])
	if err != nil {
		return 0, err
	}
	if n != len(buf) {
		return 0, fmt.Errorf("PIDTASKINFO returned %d bytes, want %d", n, len(buf))
	}
	freq, err := unix.SysctlUint64("hw.tbfrequency")
	if err != nil {
		return 0, err
	}
	u, s := binary.LittleEndian.Uint64(buf[16:24]), binary.LittleEndian.Uint64(buf[24:32])
	if math.MaxUint64-u < s {
		return 0, fmt.Errorf("CPU tick total overflows")
	}
	return tickDuration(u+s, freq)
}

func tickDuration(ticks, frequency uint64) (time.Duration, error) {
	if frequency == 0 {
		return 0, fmt.Errorf("CPU timebase frequency is zero")
	}
	seconds, remainder := ticks/frequency, ticks%frequency
	if seconds > uint64(math.MaxInt64)/uint64(time.Second) || remainder > math.MaxUint64/uint64(time.Second) {
		return 0, fmt.Errorf("CPU duration overflows")
	}
	ns := seconds*uint64(time.Second) + remainder*uint64(time.Second)/frequency
	if ns > math.MaxInt64 {
		return 0, fmt.Errorf("CPU duration overflows")
	}
	return time.Duration(ns), nil
}

// OpenFiles returns vnode paths for descriptor numbers in [0,maxFD]. A
// descriptor closed during the pass is absent evidence, not a read of its bytes.
func OpenFiles(pid, maxFD int) ([]string, error) {
	started, err := Start(pid)
	if err != nil {
		return nil, err
	}
	buf, err := descriptorInventory(pid)
	if err != nil {
		return nil, err
	}
	files, infoErr := descriptorPaths(pid, maxFD, buf)
	current, err := Start(pid)
	if err != nil {
		return nil, err
	}
	if !current.Equal(started) {
		return nil, unix.ESRCH
	}
	return files, infoErr
}

func descriptorPaths(pid, maxFD int, buf []byte) ([]string, error) {
	var files []string
	readable := false
	for off := 0; off < len(buf); off += 8 {
		rawFD := binary.LittleEndian.Uint32(buf[off : off+4])
		kind := binary.LittleEndian.Uint32(buf[off+4 : off+8])
		if rawFD > math.MaxInt32 || maxFD < 0 || int(rawFD) > maxFD || kind != 1 {
			continue
		}
		path, err := descriptorPath(pid, int(rawFD))
		if errors.Is(err, libproc.ErrUnavailable) || errors.Is(err, unix.EBADF) || errors.Is(err, unix.ENOENT) {
			continue
		} // deferred close-race decision: another descriptor must prove readability
		if err != nil {
			return nil, err
		}
		readable = true
		if path != "" {
			files = append(files, path)
		}
	}
	if !readable {
		return nil, libproc.ErrUnavailable
	}
	return files, nil
}

func descriptorInventory(pid int) ([]byte, error) {
	buf := make([]byte, 256*8)
	var n int
	for {
		var err error
		n, err = readInfo(pidInfoCall, pid, listFDs, 0, buf)
		if err != nil {
			return nil, err
		}
		if n%8 != 0 {
			return nil, fmt.Errorf("PIDLISTFDS returned a partial record: %d", n)
		}
		if n < len(buf) {
			break
		}
		if len(buf) >= maxProcessFiles*8 {
			return nil, fmt.Errorf("descriptor inventory exceeds bound")
		}
		buf = make([]byte, len(buf)*2)
	}
	return buf[:n], nil
}

func descriptorPath(pid, fd int) (string, error) {
	var path [vnodePathSize]byte
	size, err := readInfo(pidFDCall, pid, vnodePathInfo, fd, path[:])
	if err != nil {
		return "", err
	}
	if size != len(path) {
		return "", fmt.Errorf("VNODEPATHINFO returned %d bytes, want %d", size, len(path))
	}
	end := bytes.IndexByte(path[vnodePathOffset:], 0)
	if end < 0 {
		return "", fmt.Errorf("unterminated vnode path")
	}
	return string(path[vnodePathOffset : vnodePathOffset+end]), nil
}
