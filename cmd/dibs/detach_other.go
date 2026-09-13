//go:build !unix && !windows

package main

import "os/exec"

// detach on a platform with neither sessions nor process groups leaves the
// child attached. No such platform is built today; this exists so the file
// set is total rather than so anything runs here.
func detach(cmd *exec.Cmd) { _ = cmd }
