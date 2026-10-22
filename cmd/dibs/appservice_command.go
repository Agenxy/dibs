// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import "os/exec"

// The execution boundary lets fixture builds observe the actual service command
// without ever registering an app in the developer's real ServiceManagement.
var executeAppServiceCommand = func(cmd *exec.Cmd) ([]byte, error) {
	return cmd.CombinedOutput()
}
