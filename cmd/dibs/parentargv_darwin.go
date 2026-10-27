// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin

package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// parentArgv reads the parent's arguments from the kernel. No subprocess: a
// forked ps on the bridge's startup path is what #395 measured stalling.
func parentArgv() []string {
	raw, err := unix.SysctlRaw("kern.procargs2", os.Getppid())
	if err != nil {
		return nil
	}
	return parseProcargs2(raw)
}

// parseProcargs2 reads KERN_PROCARGS2: a native int argc, the executable
// path, NUL padding, then argc NUL-terminated arguments (the environment
// follows and is not read).
func parseProcargs2(raw []byte) []string {
	if len(raw) < 4 {
		return nil
	}
	argc := int(binary.LittleEndian.Uint32(raw[:4]))
	rest := raw[4:]
	end := bytes.IndexByte(rest, 0)
	if end < 0 {
		return nil
	}
	rest = rest[end:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	argv := make([]string, 0, argc)
	for len(argv) < argc && len(rest) > 0 {
		end = bytes.IndexByte(rest, 0)
		if end < 0 {
			argv = append(argv, string(rest))
			break
		}
		argv = append(argv, string(rest[:end]))
		rest = rest[end+1:]
	}
	return argv
}

// processExecutable reads another process's executable path from the same
// kernel record, without a subprocess or a permission prompt. It is the path
// the process was started from, which stays true after the file is removed.
func processExecutable(pid int) (string, error) {
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return "", err
	}
	if len(raw) < 4 {
		return "", errors.New("process arguments are truncated")
	}
	rest := raw[4:]
	end := bytes.IndexByte(rest, 0)
	if end <= 0 {
		return "", errors.New("process arguments carry no executable path")
	}
	return string(rest[:end]), nil
}
