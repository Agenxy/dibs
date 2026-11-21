// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"path/filepath"
	"strings"

	"github.com/agenxy/dibs/internal/procinfo"
)

// AppShellThreadOwner reports whether a bridge is reached from the local
// app-server through shell tooling alone. A descendant harness inherits
// CODEX_THREAD_ID too, so merely finding ChatGPT.app somewhere above it is
// insufficient. An unreadable hop cannot establish ownership.
func AppShellThreadOwner(pid int) bool {
	child, err := procinfo.Inspect(pid)
	if err != nil {
		return false
	}
	for range 6 {
		parent, path, ok := observedShellParent(child)
		if !ok {
			return false
		}
		name := filepath.Base(path)
		if name == "codex" {
			return localAppServerProcess(parent, path)
		}
		if !appShellTool(name) {
			return false
		}
		child = parent
	}
	return false
}

func observedShellParent(child procinfo.Process) (procinfo.Process, string, bool) {
	parent, err := procinfo.Inspect(child.PPID)
	if err != nil || parent.PID <= 1 || parent.Start.After(child.Start) {
		return procinfo.Process{}, "", false
	}
	path, err := procinfo.Path(parent.PID)
	if err != nil {
		return procinfo.Process{}, "", false
	}
	same, err := procinfo.SameProcess(parent)
	return parent, filepath.ToSlash(path), path != "" && same && err == nil
}

func appShellTool(name string) bool {
	name = strings.ToLower(name)
	switch name {
	case "sh", "bash", "zsh", "dash", "ksh", "fish", "python":
		return true
	}
	// Native Python paths can end in Python, python3, or python3.X.
	version, ok := strings.CutPrefix(name, "python")
	return ok && version != "" && strings.Trim(version, "0123456789.") == ""
}

func localAppServerProcess(row procinfo.Process, path string) bool {
	prefix, _, bundled := strings.Cut(path, "/ChatGPT.app/")
	if !bundled {
		return false
	}
	_, parentPath, ok := observedShellParent(row)
	if !ok || !strings.HasPrefix(parentPath, prefix+"/ChatGPT.app/") || filepath.Base(parentPath) == "codex" {
		return false
	}
	args, err := procinfo.Command(row.PID)
	if err != nil || len(args) == 0 || !appServerCommand(args[1:]) {
		return false
	}
	same, err := procinfo.SameProcess(row)
	return err == nil && same
}

func appServerCommand(args []string) bool {
	for len(args) > 0 {
		switch args[0] {
		case "app-server":
			return true
		case "-c", "--config", "--enable", "--disable":
			if len(args) < 2 {
				return false
			}
			args = args[2:]
		default:
			return false
		}
	}
	return false
}
