//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// detach on Windows: a new process group and no console, so the daemon
// survives this CLI exiting and does not take a Ctrl-C aimed at the
// terminal with it, which is what setsid buys on unix.
func detach(cmd *exec.Cmd) {
	const detachedProcess = 0x00000008
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess,
	}
}
