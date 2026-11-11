// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"path/filepath"
	"strings"

	"github.com/agenxy/dibs/internal/procinfo"
)

// Positive native ancestry only. Local host/cwd observations remain valid:
// this executor runs tools here, while its conversation lives in the cloud.
// Unknown command evidence preserves the app surface, never invents cloud.
func (p *processPass) cloudExecutionAncestor(pid, depth int) bool {
	seen := map[int]bool{}
	for range depth {
		parent := p.rows[pid].parent
		if parent <= 1 || seen[parent] || p.ctx.Err() != nil {
			return false
		}
		seen[parent] = true
		path := p.path(parent)
		if p.native && path.state == processPresent && filepath.Base(path.value) == "codex" &&
			strings.Contains(path.value, "/ChatGPT.app/") {
			args, err := procinfo.Command(parent)
			present, sameErr := p.present(parent)
			// The nearest runtime owns this bridge. A local app-server may
			// itself have been launched by an outer cloud executor; inheriting
			// that outer provenance would falsely disable its local binding.
			return err == nil && sameErr == nil && present && cloudExecutionCommand(args)
		}
		pid = parent
	}
	return false
}

func cloudExecutionCommand(args []string) bool {
	executor := false
	for i, arg := range args {
		if arg == "exec-server" {
			executor = true
		}
		if executor && ((arg == "--remote" && i+1 < len(args) && args[i+1] != "") ||
			(strings.HasPrefix(arg, "--remote=") && len(arg) > len("--remote="))) {
			return true
		}
	}
	return false
}
