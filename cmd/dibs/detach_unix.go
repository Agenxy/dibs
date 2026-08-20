//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// detach puts the daemon in its own session, so it survives this CLI exiting
// and does not take a Ctrl-C aimed at the terminal with it. Only reached when
// there is no service unit: a supervised daemon is restarted through its own
// manager, which does this properly.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
