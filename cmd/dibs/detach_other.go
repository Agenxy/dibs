//go:build !unix

package main

import "os/exec"

// detach on a platform without process groups leaves the child attached:
// `dibs upgrade` still restarts the daemon, and the daemon stays a child of
// the CLI that started it, which is the one Windows behaviour nobody has
// yet asked for or measured (README, "Windows").
func detach(cmd *exec.Cmd) { _ = cmd }
