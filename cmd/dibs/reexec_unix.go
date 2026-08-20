//go:build unix

package main

import (
	"os"
	"syscall"
)

// reexec replaces this process image with the binary now on disk, keeping the
// pid and every file descriptor. It returns only on failure.
func reexec(path string, env []string) error {
	// #nosec G204,G702 -- path is os.Executable(): this process re-executing its
	// own binary at its own path. argv and the environment are this process's
	// own, plus one key it wrote itself. No caller-supplied text reaches any of
	// them, which is what the taint analysis cannot see through os.Executable.
	return syscall.Exec(path, os.Args, env)
}
